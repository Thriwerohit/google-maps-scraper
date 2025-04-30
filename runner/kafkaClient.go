package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"github.com/gosom/google-maps-scraper/utils"
	"github.com/linkedin/goavro"
	"github.com/riferrei/srclient"
	"github.com/xdg-go/scram"
	"go.uber.org/zap"
)

type kafkaClient struct {
	Producer             sarama.SyncProducer
	Consumer             sarama.ConsumerGroup
	SchemaRegistryClient *srclient.SchemaRegistryClient
	Codec                *goavro.Codec
	sugar                *zap.SugaredLogger
}

type KafkaClient interface {
	ProduceMessage(message interface{}, topic, subject string, traceId, bookingId string) error
	Close()
}

// XDGSCRAMClient is an implementation of the SCRAMClient interface for SCRAM-SHA-512
type XDGSCRAMClient struct {
	*scram.Client
	*scram.ClientConversation
	HashGeneratorFcn scram.HashGeneratorFcn
}

// Begin prepares the client to start the SCRAM conversation
func (x *XDGSCRAMClient) Begin(userName, password, authzID string) (err error) {
	x.Client, err = x.HashGeneratorFcn.NewClient(userName, password, authzID)
	if err != nil {
		return err
	}
	x.ClientConversation = x.Client.NewConversation()
	return nil
}

// Step advances the SCRAM conversation
func (x *XDGSCRAMClient) Step(challenge string) (response string, err error) {
	return x.ClientConversation.Step(challenge)
}

// Done checks if the SCRAM conversation is complete
func (x *XDGSCRAMClient) Done() bool {
	return x.ClientConversation.Done()
}

func NewKafkaClient(kafkaConfig KafkaConfig, sugar *zap.SugaredLogger) (KafkaClient, error) {
	//var SHA512 scram.HashGeneratorFcn = sha512.New
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true
	config.Producer.Return.Errors = true
	config.Consumer.Offsets.Retry.Max = 1
	config.Consumer.Offsets.AutoCommit.Enable = true // Enable auto-commit
	//Set SASL/SCRAM authentication details
	// config.Net.SASL.Enable = true
	// config.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA512
	// config.Net.SASL.User = kafkaConfig.SASLUser
	// config.Net.SASL.Password = kafkaConfig.SASLPassword
	// config.Net.TLS.Enable = true
	// config.Version = sarama.V3_5_0_0
	// config.Net.TLS.Config = &tls.Config{
	// 	InsecureSkipVerify: true,
	// }

	// Set the SCRAMClientGeneratorFunc for SCRAM authentication
	//config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }

	// Create Kafka producer
	producer, err := sarama.NewSyncProducer(kafkaConfig.Brokers, config)
	if err != nil {
		sugar.Errorf("Error creating Kafka producer: %v", err)
		return nil, fmt.Errorf("error creating producer: %v", err)
	}

	// Create Kafka consumer
	groupId := "scapper-consumer-group"
	consumer, err := sarama.NewConsumerGroup(kafkaConfig.Brokers, groupId, config)
	if err != nil {
		sugar.Errorf("Error creating Kafka consumer: %v", err)
		return nil, fmt.Errorf("error creating consumer: %v", err)
	}

	// Create Schema Registry client
	schemaRegistryClient := srclient.CreateSchemaRegistryClient(kafkaConfig.SchemaRegistryUrl)
	if schemaRegistryClient == nil {
		err := errors.New("failed to connect to schema registry")
		sugar.Error(err)
		return nil, err
	}

	sugar.Info("Kafka client successfully initialized")

	return &kafkaClient{
		Producer:             producer,
		Consumer:             consumer,
		SchemaRegistryClient: schemaRegistryClient,
		sugar:                sugar,
	}, nil
}

func (kc *kafkaClient) Close() {
	if err := kc.Producer.Close(); err != nil {
		kc.sugar.Errorf("Failed to close Kafka producer: %v", err)
	}
	if err := kc.Consumer.Close(); err != nil {
		kc.sugar.Errorf("Failed to close Kafka consumer: %v", err)
	}
}

func (kc *kafkaClient) ProduceMessage(message interface{}, topic, subject string, traceId, bookingId string) error {
	var log utils.LogEntry
	log.TraceID = traceId
	log.Step = "Produce message for data sync workflow"
	log.Method = "ProduceMessageDataSync"
	requestBody, errMarshal := json.Marshal(message)
	if errMarshal != nil {
		log.Status = "failed"
		log.ErrorDescription = errMarshal.Error()
		utils.LoggerData(kc.sugar, log)
	}
	log.RequestBody = string(requestBody)
	log.CreatedAt = time.Now()

	schema, err := kc.SchemaRegistryClient.GetLatestSchema(subject)
	if err != nil {
		log.Status = "failed"
		log.ErrorDescription = "Failed to fetch schema from schema registry: " + err.Error()
		utils.LoggerData(kc.sugar, log)
		return fmt.Errorf("failed to fetch schema: %w", err)
	}

	schemaString := schema.Schema()

	// Create Avro codec
	codec, err := goavro.NewCodec(schemaString)
	if err != nil {
		log.Status = "failed"
		log.ErrorDescription = "Failed to create Avro codec: " + err.Error()
		utils.LoggerData(kc.sugar, log)

		return fmt.Errorf("failed to create Avro codec: %w", err)
	}

	avroBytes, err := codec.BinaryFromNative(nil, message)
	if err != nil {
		log.Status = "failed"
		log.ErrorDescription = "Failed to encode message to Avro: " + err.Error()
		utils.LoggerData(kc.sugar, log)
		return fmt.Errorf("failed to encode message: %w", err)
	}

	kafkaMessage := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(avroBytes),
		Key:   sarama.ByteEncoder([]byte(bookingId)),
	}

	partition, offset, err := kc.Producer.SendMessage(kafkaMessage)
	if err != nil {
		log.Status = "failed"
		log.ErrorDescription = "Failed to send message to Kafka: " + err.Error()
		utils.LoggerData(kc.sugar, log)
		return fmt.Errorf("failed to send message: %w", err)
	}
	log.Status = "success"
	log.ResponseBody = fmt.Sprintf("Message successfully sent to topic(%s)/partition(%d)/offset(%d)", topic, partition, offset)
	utils.LoggerData(kc.sugar, log)

	return nil
}

