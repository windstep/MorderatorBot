package config

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"os"
)

type Config struct {
	VkBotToken        string `mapstructure:"vk_bot_token"`
	VkGroupId         int    `mapstructure:"vk_group_id"`
	DbFile            string `mapstructure:"db"`
	BlocklistFilename string `mapstructure:"blocklist_file"`
}

func Init() *Config {
	var cfg Config

	viper.SetConfigFile("config/config.yml")
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Не могу прочитать в конфигурацию: %+v", err)
	}

	if _, err := os.Stat(".env"); err == os.ErrNotExist {
		log.Fatal("Ошибка: файла .env не существует. Скопируйте файл .env.example в новый файл .env и заполните нужные переменные")
	}
	viper.SetConfigFile(".env")
	if err := viper.MergeInConfig(); err != nil {
		log.Fatalf("Ошибка слияния конфигураци: %+v", err)
	}

	if err := viper.Unmarshal(&cfg); err != nil {
		log.Fatalf("Не могу разобрать конфигурацию в переменную: %+v", err)
	}

	return &cfg
}
