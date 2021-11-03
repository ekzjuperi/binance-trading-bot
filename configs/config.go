package configs

import (
	"os"

	"gopkg.in/yaml.v2"
)

type BotConfig struct {
	APIKey    string `yaml:"api-key"`
	SecretKey string `yaml:"secret-key"`
	Symbol    string `yaml:"symbol"`
	Port      string `yaml:"port"`
}

func GetConfig() (*BotConfig, error) {
	f, err := os.Open("configs/config.yaml")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg BotConfig

	decoder := yaml.NewDecoder(f)

	err = decoder.Decode(&cfg)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}
