package config

import (
	"log"
	"sync"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/spf13/viper"
)

var configInstance *runner.Config
var once sync.Once

func Init() *runner.Config {
	once.Do(func() {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath("config/")
		viper.AutomaticEnv()

		if err := viper.ReadInConfig(); err != nil {
			log.Fatalf("Error reading config file: %v", err)
		}

		config := new(runner.Config)
		if err := viper.Unmarshal(&config); err != nil {
			log.Fatalf("Unable to unmarshal config into struct: %v", err)
		}
		configInstance = config
	})

	return configInstance

}
