package lilith

import (
	"context"
	"net/http"
)

// WeatherReport is a domain summary of current weather conditions.
type WeatherReport struct {
	LocationName string
	Country      string
	Description  string
	Temperature  int
	FeelsLike    int
	Humidity     int
	WindSpeed    int
	WindDir      string
}

//go:generate go tool moq -out internal/mock/weather_provider.go -pkg mock . WeatherProvider

// WeatherProvider returns current weather, used by the AI layer as a tool.
type WeatherProvider interface {
	Current(ctx context.Context, city, countryCode string) (*WeatherReport, error)
}

//go:generate go tool moq -out internal/mock/http_client.go -pkg mock . HTTPClient

// HTTPClient is the interface satisfied by *http.Client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}
