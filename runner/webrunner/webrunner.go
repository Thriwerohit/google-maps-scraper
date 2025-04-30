package webrunner

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/tlmt"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/sqlite"
	"github.com/gosom/scrapemate"
	"github.com/gosom/scrapemate/adapters/writers/csvwriter"
	"github.com/gosom/scrapemate/scrapemateapp"
	"golang.org/x/sync/errgroup"
)

type webrunner struct {
	srv *web.Server
	svc *web.Service
	cfg *runner.Config
}

func New(cfg *runner.Config) (runner.Runner, error) {
	if cfg.DataFolder == "" {
		return nil, fmt.Errorf("data folder is required")
	}

	if err := os.MkdirAll(cfg.DataFolder, os.ModePerm); err != nil {
		return nil, err
	}

	repo, err := sqlite.New(cfg.Databases.Discovery.URI)
	if err != nil {
		return nil, err
	}

	svc := web.NewService(repo, cfg.DataFolder)

	srv, err := web.New(svc, cfg.Addr)
	if err != nil {
		return nil, err
	}

	ans := webrunner{
		srv: srv,
		svc: svc,
		cfg: cfg,
	}

	return &ans, nil
}

func (w *webrunner) Run(ctx context.Context) error {
	egroup, ctx := errgroup.WithContext(ctx)

	egroup.Go(func() error {
		return w.work(ctx)
	})

	egroup.Go(func() error {
		return w.srv.Start(ctx)
	})

	return egroup.Wait()
}

func (w *webrunner) Close(context.Context) error {
	return nil
}

func (w *webrunner) work(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			jobs, err := w.svc.SelectPending(ctx)
			if err != nil {
				return err
			}

			for i := range jobs {
				select {
				case <-ctx.Done():
					return nil
				default:
					t0 := time.Now().UTC()
					if err := w.scrapeJob(ctx, &jobs[i]); err != nil {
						params := map[string]any{
							"job_count": len(jobs[i].Data.Keywords),
							"duration":  time.Now().UTC().Sub(t0).String(),
							"error":     err.Error(),
						}

						evt := tlmt.NewEvent("web_runner", params)

						_ = runner.Telemetry().Send(ctx, evt)

						log.Printf("error scraping job %s: %v", jobs[i].ID, err)
					} else {
						params := map[string]any{
							"job_count": len(jobs[i].Data.Keywords),
							"duration":  time.Now().UTC().Sub(t0).String(),
						}

						_ = runner.Telemetry().Send(ctx, tlmt.NewEvent("web_runner", params))

						log.Printf("job %s scraped successfully", jobs[i].ID)
					}
				}
			}
		}
	}
}

func (w *webrunner) scrapeJob(ctx context.Context, job *web.Job) error {
	job.Status = web.StatusWorking

	err := w.svc.Update(ctx, job)
	if err != nil {
		return err
	}

	if len(job.Data.Keywords) == 0 {
		job.Status = web.StatusFailed

		return w.svc.Update(ctx, job)
	}

	outpath := filepath.Join(w.cfg.DataFolder, job.ID+".csv")

	outfile, err := os.Create(outpath)
	if err != nil {
		return err
	}

	defer func() {
		_ = outfile.Close()
	}()

	mate, err := w.setupMate(ctx, outfile, job)
	if err != nil {
		job.Status = web.StatusFailed

		err2 := w.svc.Update(ctx, job)
		if err2 != nil {
			log.Printf("failed to update job status: %v", err2)
		}

		return err
	}

	defer mate.Close()

	var coords string
	if job.Data.Lat != "" && job.Data.Lon != "" {
		coords = job.Data.Lat + "," + job.Data.Lon
	}

	dedup := deduper.New()
	exitMonitor := exiter.New()

	seedJobs, err := runner.CreateSeedJobs(
		job.Data.FastMode,
		job.Data.Lang,
		strings.NewReader(strings.Join(job.Data.Keywords, "\n")),
		job.Data.Depth,
		job.Data.Email,
		coords,
		job.Data.Zoom,
		func() float64 {
			if job.Data.Radius <= 0 {
				return 10000 // 10 km
			}

			return float64(job.Data.Radius)
		}(),
		dedup,
		exitMonitor,
	)
	if err != nil {
		err2 := w.svc.Update(ctx, job)
		if err2 != nil {
			log.Printf("failed to update job status: %v", err2)
		}

		return err
	}

	if len(seedJobs) > 0 {
		exitMonitor.SetSeedCount(len(seedJobs))

		allowedSeconds := max(60, len(seedJobs)*10*job.Data.Depth/50+120)

		if job.Data.MaxTime > 0 {
			if job.Data.MaxTime.Seconds() < 180 {
				allowedSeconds = 180
			} else {
				allowedSeconds = int(job.Data.MaxTime.Seconds())
			}
		}

		log.Printf("running job %s with %d seed jobs and %d allowed seconds", job.ID, len(seedJobs), allowedSeconds)

		mateCtx, cancel := context.WithTimeout(ctx, time.Duration(allowedSeconds)*time.Second)
		defer cancel()

		exitMonitor.SetCancelFunc(cancel)

		go exitMonitor.Run(mateCtx)

		err = mate.Start(mateCtx, seedJobs...)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			cancel()

			err2 := w.svc.Update(ctx, job)
			if err2 != nil {
				log.Printf("failed to update job status: %v", err2)
			}

			return err
		}

		cancel()
	}

	mate.Close()

	job.Status = web.StatusOK

	places, err := ParseCSVToStructs(outpath)
	if err != nil {
		log.Fatalf("failed parsing csv: %v", err)
	}

	var updateRequest UpdateInput
	updateRequest.ID = job.Data.FacilityId
	updateRequest.NewRating = places[0].Rating
	updateRequest.NewReviewCnt = int64(places[0].Reviews)
	err = UpdateRatingsAndReviews(ctx, updateRequest, *w.cfg.MongoClient)
	if err != nil {
		log.Printf("failed updating ratings and reviews: %v", err)
		job.Status = web.StatusFailed
	}

	return w.svc.Update(ctx, job)
}

func (w *webrunner) setupMate(_ context.Context, writer io.Writer, job *web.Job) (*scrapemateapp.ScrapemateApp, error) {
	opts := []func(*scrapemateapp.Config) error{
		scrapemateapp.WithConcurrency(w.cfg.Concurrency),
		scrapemateapp.WithExitOnInactivity(time.Minute * 3),
	}

	if !job.Data.FastMode {
		opts = append(opts,
			scrapemateapp.WithJS(scrapemateapp.DisableImages()),
		)
	} else {
		opts = append(opts,
			scrapemateapp.WithStealth("firefox"),
		)
	}

	hasProxy := false

	if len(w.cfg.Proxies) > 0 {
		opts = append(opts, scrapemateapp.WithProxies(w.cfg.Proxies))
		hasProxy = true
	} else if len(job.Data.Proxies) > 0 {
		opts = append(opts,
			scrapemateapp.WithProxies(job.Data.Proxies),
		)
		hasProxy = true
	}

	if !w.cfg.DisablePageReuse {
		opts = append(opts,
			scrapemateapp.WithPageReuseLimit(2),
			scrapemateapp.WithPageReuseLimit(200),
		)
	}

	log.Printf("job %s has proxy: %v", job.ID, hasProxy)

	csvWriter := csvwriter.NewCsvWriter(csv.NewWriter(writer))

	writers := []scrapemate.ResultWriter{csvWriter}

	matecfg, err := scrapemateapp.NewConfig(
		writers,
		opts...,
	)
	if err != nil {
		return nil, err
	}

	return scrapemateapp.NewScrapeMateApp(matecfg)
}

func ParseCSVToStructs(filePath string) ([]PlaceData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}

	var places []PlaceData

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		row := make(map[string]string)
		for i, value := range record {
			row[headers[i]] = value
		}

		var place PlaceData

		// Direct string assignments
		place.InputID = row["input_id"]
		place.Link = row["link"]
		place.Title = row["title"]
		place.Category = row["category"]
		place.Address = row["address"]
		place.Website = row["website"]
		place.Phone = row["phone"]
		place.PlusCode = row["plus_code"]
		place.OwnerID = row["owner_id"]
		place.ReviewSummary = row["review_summary"]
		place.WorkingHoursOld = row["working_hours_old"]
		place.Status = row["status"]
		place.UpdatedAt = row["updated_at"]
		place.DataID = row["data_id"]
		place.Emails = row["emails"]

		// Parse floats and ints
		place.Rating, _ = strconv.ParseFloat(row["review_rating"], 64)
		place.Reviews, _ = strconv.Atoi(row["review_count"])
		place.Latitude, _ = strconv.ParseFloat(row["latitude"], 64)
		place.Longitude, _ = strconv.ParseFloat(row["longitude"], 64)
		place.Verified = row["verified"] == "true"

		// JSON fields
		_ = json.Unmarshal([]byte(row["open_hours"]), &place.OpenHours)
		_ = json.Unmarshal([]byte(row["popular_times"]), &place.PopularTimes)
		_ = json.Unmarshal([]byte(row["images"]), &place.Images)
		_ = json.Unmarshal([]byte(row["reservations"]), &place.Reservations)
		_ = json.Unmarshal([]byte(row["order_online"]), &place.OrderOnline)
		_ = json.Unmarshal([]byte(row["menu"]), &place.Menu)
		_ = json.Unmarshal([]byte(row["owner"]), &place.Owner)
		_ = json.Unmarshal([]byte(row["complete_address"]), &place.CompleteAddress)
		_ = json.Unmarshal([]byte(row["about"]), &place.About)
		_ = json.Unmarshal([]byte(row["user_reviews"]), &place.UserReviews)

		places = append(places, place)
	}

	return places, nil
}

type PlaceData struct {
	InputID         string                    `json:"input_id"`
	Link            string                    `json:"link"`
	Title           string                    `json:"title"`
	Category        string                    `json:"category"`
	Address         string                    `json:"address"`
	OpenHours       map[string][]string       `json:"open_hours"`
	PopularTimes    map[string]map[string]int `json:"popular_times"`
	Website         string                    `json:"website"`
	Phone           string                    `json:"phone"`
	PlusCode        string                    `json:"plus_code"`
	Rating          float64                   `json:"rating"`
	Reviews         int                       `json:"reviews"`
	Latitude        float64                   `json:"latitude"`
	Longitude       float64                   `json:"longitude"`
	OwnerID         string                    `json:"owner_id"`
	Verified        bool                      `json:"verified"`
	ReviewSummary   string                    `json:"review_summary"`
	WorkingHoursOld string                    `json:"working_hours_old"`
	Status          string                    `json:"status"`
	UpdatedAt       string                    `json:"updated_at"`
	DataID          string                    `json:"data_id"`
	Images          []ImageEntry              `json:"images"`
	Reservations    []LinkEntry               `json:"reservations"`
	OrderOnline     []LinkEntry               `json:"order_online"`
	Menu            MenuInfo                  `json:"menu"`
	Owner           OwnerInfo                 `json:"owner"`
	CompleteAddress AddressInfo               `json:"complete_address"`
	About           []AboutEntry              `json:"about"`
	UserReviews     []UserReview              `json:"user_reviews"`
	Emails          string                    `json:"emails"`
}

// Sub-structs
type ImageEntry struct {
	Title string `json:"title"`
	Image string `json:"image"`
}

type LinkEntry struct {
	Link string `json:"link"`
}

type MenuInfo struct {
	Link   string `json:"link"`
	Source string `json:"source"`
}

type OwnerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AddressInfo struct {
	Borough    string `json:"borough"`
	District   string `json:"district"`
	City       string `json:"city"`
	State      string `json:"state"`
	Country    string `json:"country"`
	PostalCode string `json:"postal_code"`
	Raw        string `json:"raw"`
	Formatted  string `json:"formatted"`
}

type AboutEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type UserReview struct {
	Name           string `json:"Name"`
	ProfilePicture string `json:"ProfilePicture"`
	Review         string `json:"Review"`
	Rating         int    `json:"Rating"`
}
