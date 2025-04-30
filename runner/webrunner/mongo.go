package webrunner

import (
	"context"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type UpdateInput struct {
	ID           string
	NewRating    float64
	NewReviewCnt int64
}

func UpdateRatingsAndReviews(ctx context.Context, input UpdateInput, mongoDb mongo.Database) error {
	filter := bson.M{"_id": input.ID}

	update := bson.M{
		"$set": bson.M{
			"ratings":     input.NewRating,
			"noOfReviews": input.NewReviewCnt,
			"updatedAt":   time.Now(),
		},
	}

	result, err := mongoDb.Collection("facilities").UpdateOne(ctx, filter, update)
	log.Println("Update result:", result)
	return err
}
