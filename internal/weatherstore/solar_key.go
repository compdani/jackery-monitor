package weatherstore

import (
	"database/sql"
	"errors"
	"strings"
)

func (p PocketWeatherStore) SaveSolarKey(encryptionKey []byte, apiKey string) error {
	return p.Store.SaveSecret(encryptionKey, "forecast_solar", map[string]any{"api_key": strings.TrimSpace(apiKey)})
}
func (p PocketWeatherStore) SolarKey(encryptionKey []byte) (string, error) {
	v, err := p.Store.Secret(encryptionKey, "forecast_solar")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	key, _ := v["api_key"].(string)
	return strings.TrimSpace(key), nil
}
func (p PocketWeatherStore) ClearSolarKey() error {
	err := p.Store.Delete("credentials", "forecast_solar")
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
