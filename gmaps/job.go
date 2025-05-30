package gmaps

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/scrapemate"
	"github.com/playwright-community/playwright-go"
)

type GmapJobOptions func(*GmapJob)

type GmapJob struct {
	scrapemate.Job

	MaxDepth     int
	LangCode     string
	ExtractEmail bool

	Deduper     deduper.Deduper
	ExitMonitor exiter.Exiter
}

func NewGmapJob(
	id, langCode, query string,
	maxDepth int,
	extractEmail bool,
	geoCoordinates string,
	zoom int,
	opts ...GmapJobOptions,
) *GmapJob {
	const (
		maxRetries = 3
		prio       = scrapemate.PriorityLow
	)

	isUrl := regex(query)
	if isUrl {
		job := GmapJob{
			Job: scrapemate.Job{
				ID:         id,
				Method:     http.MethodGet,
				URL:        query,
				URLParams:  map[string]string{"hl": langCode},
				MaxRetries: maxRetries,
				Priority:   prio,
			},
			MaxDepth:     maxDepth,
			LangCode:     langCode,
			ExtractEmail: extractEmail,
		}

		for _, opt := range opts {
			opt(&job)
		}

		return &job
	}
	query = url.QueryEscape(query)

	if id == "" {
		id = uuid.New().String()
	}

	mapURL := ""
	if geoCoordinates != "0" && zoom > 0 {
		mapURL = fmt.Sprintf("https://www.google.com/maps/search/%s/@%s,%dz", query, strings.ReplaceAll(geoCoordinates, " ", ""), zoom)
	} else {
		//Warning: geo and zoom MUST be both set or not
		mapURL = fmt.Sprintf("https://www.google.com/maps/search/%s", query)
	}

	job := GmapJob{
		Job: scrapemate.Job{
			ID:         id,
			Method:     http.MethodGet,
			URL:        mapURL,
			URLParams:  map[string]string{"hl": langCode},
			MaxRetries: maxRetries,
			Priority:   prio,
		},
		MaxDepth:     maxDepth,
		LangCode:     langCode,
		ExtractEmail: extractEmail,
	}

	for _, opt := range opts {
		opt(&job)
	}

	return &job
}

func WithDeduper(d deduper.Deduper) GmapJobOptions {
	return func(j *GmapJob) {
		j.Deduper = d
	}
}

func WithExitMonitor(e exiter.Exiter) GmapJobOptions {
	return func(j *GmapJob) {
		j.ExitMonitor = e
	}
}

func (j *GmapJob) UseInResults() bool {
	return false
}

func (j *GmapJob) Process(ctx context.Context, resp *scrapemate.Response) (any, []scrapemate.IJob, error) {
	defer func() {
		resp.Document = nil
		resp.Body = nil
	}()

	log := scrapemate.GetLoggerFromContext(ctx)

	doc, ok := resp.Document.(*goquery.Document)
	if !ok {
		return nil, nil, fmt.Errorf("could not convert to goquery document")
	}

	var next []scrapemate.IJob

	if strings.Contains(resp.URL, "/maps/place/") {
		jopts := []PlaceJobOptions{}
		if j.ExitMonitor != nil {
			jopts = append(jopts, WithPlaceJobExitMonitor(j.ExitMonitor))
		}

		placeJob := NewPlaceJob(j.ID, j.LangCode, resp.URL, j.ExtractEmail, jopts...)

		next = append(next, placeJob)
	} else {
		doc.Find(`div[role=feed] div[jsaction]>a`).Each(func(_ int, s *goquery.Selection) {
			if href := s.AttrOr("href", ""); href != "" {
				jopts := []PlaceJobOptions{}
				if j.ExitMonitor != nil {
					jopts = append(jopts, WithPlaceJobExitMonitor(j.ExitMonitor))
				}

				nextJob := NewPlaceJob(j.ID, j.LangCode, href, j.ExtractEmail, jopts...)

				if j.Deduper == nil || j.Deduper.AddIfNotExists(ctx, href) {
					next = append(next, nextJob)
				}
			}
		})
	}

	if j.ExitMonitor != nil {
		j.ExitMonitor.IncrPlacesFound(len(next))
		j.ExitMonitor.IncrSeedCompleted(1)
	}

	log.Info(fmt.Sprintf("%d places found", len(next)))

	return nil, next, nil
}

func (j *GmapJob) BrowserActions(ctx context.Context, page playwright.Page) scrapemate.Response {
	var resp scrapemate.Response

	pageResponse, err := page.Goto(j.GetFullURL(), playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})

	if err != nil {
		resp.Error = err

		return resp
	}

	if err = clickRejectCookiesIfRequired(page); err != nil {
		resp.Error = err

		return resp
	}

	const defaultTimeout = 5000

	err = page.WaitForURL(page.URL(), playwright.PageWaitForURLOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(defaultTimeout),
	})

	if err != nil {
		resp.Error = err

		return resp
	}

	resp.URL = pageResponse.URL()
	resp.StatusCode = pageResponse.Status()
	resp.Headers = make(http.Header, len(pageResponse.Headers()))

	for k, v := range pageResponse.Headers() {
		resp.Headers.Add(k, v)
	}

	// When Google Maps finds only 1 place, it slowly redirects to that place's URL
	// check element scroll
	sel := `div[role='feed']`

	//nolint:staticcheck // TODO replace with the new playwright API
	_, err = page.WaitForSelector(sel, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(500),
	})

	if err != nil {
		select {
		case <-ctx.Done():
			resp.Error = ctx.Err()

			return resp
		case <-time.After(3 * time.Second):
		}
	}

	if strings.Contains(page.URL(), "/maps/place/") {
		resp.URL = page.URL()

		var body string

		body, err = page.Content()
		if err != nil {
			resp.Error = err
			return resp
		}

		resp.Body = []byte(body)

		return resp
	}

	_, err = scroll(ctx, page, j.MaxDepth)
	if err != nil {
		resp.Error = err

		return resp
	}

	body, err := page.Content()
	if err != nil {
		resp.Error = err
		return resp
	}

	resp.Body = []byte(body)

	return resp
}

func clickRejectCookiesIfRequired(page playwright.Page) error {
	// click the cookie reject button if exists
	sel := `form[action="https://consent.google.com/save"]:first-of-type button:first-of-type`

	const timeout = 500

	//nolint:staticcheck // TODO replace with the new playwright API
	el, err := page.WaitForSelector(sel, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(timeout),
	})

	if err != nil {
		return nil
	}

	if el == nil {
		return nil
	}

	//nolint:staticcheck // TODO replace with the new playwright API
	return el.Click()
}

func scroll(ctx context.Context, page playwright.Page, maxDepth int) (int, error) {
	scrollSelector := `div[role='feed']`
	expr := `async () => {
		const el = document.querySelector("` + scrollSelector + `");
		el.scrollTop = el.scrollHeight;

		return new Promise((resolve, reject) => {
  			setTimeout(() => {
    		resolve(el.scrollHeight);
  			}, %d);
		});
	}`

	var currentScrollHeight int
	// Scroll to the bottom of the page.
	waitTime := 100.
	cnt := 0

	const (
		timeout  = 500
		maxWait2 = 2000
	)

	for i := 0; i < maxDepth; i++ {
		cnt++
		waitTime2 := timeout * cnt

		if waitTime2 > timeout {
			waitTime2 = maxWait2
		}

		// Scroll to the bottom of the page.
		scrollHeight, err := page.Evaluate(fmt.Sprintf(expr, waitTime2))
		if err != nil {
			return cnt, err
		}

		height, ok := scrollHeight.(int)
		if !ok {
			return cnt, fmt.Errorf("scrollHeight is not an int")
		}

		if height == currentScrollHeight {
			break
		}

		currentScrollHeight = height

		select {
		case <-ctx.Done():
			return currentScrollHeight, nil
		default:
		}

		waitTime *= 1.5

		if waitTime > maxWait2 {
			waitTime = maxWait2
		}

		//nolint:staticcheck // TODO replace with the new playwright API
		page.WaitForTimeout(waitTime)
	}

	return cnt, nil
}

func regex(text string) bool {

	pattern := `https?://[^\s]+`
	matched, _ := regexp.MatchString(pattern, text)
	if matched {
		return true
	}
	return false
}
