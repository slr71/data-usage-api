package config

import (
	"errors"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	DBURI    string
	DBSchema string

	ICATURI           string
	Zone              string
	RootResourceNames []string

	UserSuffix      string
	RefreshInterval *time.Duration
}

func NewFromViper(cfg *viper.Viper) (*Config, error) {
	ri, err := time.ParseDuration(cfg.GetString("dataUsageApi.refreshInterval"))
	if err != nil {
		return nil, err
	}
	c := &Config{
		DBURI:             cfg.GetString("db.uri"),
		DBSchema:          cfg.GetString("db.schema"),
		ICATURI:           cfg.GetString("icat.uri"),
		Zone:              cfg.GetString("icat.zone"),
		RootResourceNames: cfg.GetStringSlice("icat.rootResources"),
		UserSuffix:        cfg.GetString("users.domain"),
		RefreshInterval:   &ri,
	}

	err = c.Validate()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Validate() error {
	if c.DBURI == "" {
		return errors.New("db.uri must be set")
	}

	if c.DBSchema == "" {
		return errors.New("db.schema must be set")
	}

	if c.ICATURI == "" {
		return errors.New("icat.uri must be set")
	}

	if c.Zone == "" {
		return errors.New("icat.zone must be set")
	}

	if c.RootResourceNames == nil {
		return errors.New("icat.rootResources must be set")
	}

	if c.UserSuffix == "" {
		return errors.New("users.domain must be set")
	}

	if c.RefreshInterval == nil {
		return errors.New("refresh interval must be set")
	}

	return nil
}
