// Package ai: tool execution shared by the OpenRouter and Google backends. The
// two backends differ only in transport (how tool calls and results are encoded
// on the wire); the business logic of each tool lives here so it is defined once.
package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/reaction"
)

// toolset holds the providers backing the model's tools. The Discord provider
// and primary image generator may be nil, in which case their tools are not
// offered to the model; the fallback may be nil to disable fallback.
type toolset struct {
	weather       lilith.WeatherProvider
	discord       lilith.DiscordProvider
	image         lilith.ImageGenerator
	imageFallback lilith.ImageGenerator
}

// execute runs a single tool call by name with the given JSON arguments. It
// returns the textual result to feed back to the model and mutates result for
// side effects (reactions, images). ok is false for an unknown tool name, in
// which case the caller should not feed a result back.
func (t *toolset) execute(
	ctx context.Context,
	lg *zap.Logger,
	req lilith.ResponseRequest,
	result *lilith.ResponseResult,
	name string,
	args json.RawMessage,
) (content string, ok bool, err error) {
	switch name {
	case "reply_emoji":
		var a struct {
			Emoji string `json:"emoji"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", true, errors.Wrap(err, "unmarshal arguments")
		}

		payload, err := json.Marshal(struct {
			Emoji string `json:"reply_emoji"`
		}{Emoji: a.Emoji})
		if err != nil {
			return "", true, errors.Wrap(err, "marshal emoji")
		}

		if text, ok := reaction.Canonicalize(a.Emoji); ok {
			result.Reactions = append(result.Reactions, text)
		}

		return string(payload), true, nil

	case "get_weather":
		var a struct {
			City        string `json:"city"`
			CountryCode string `json:"country_code"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", true, errors.Wrap(err, "unmarshal arguments")
		}

		info, err := t.weather.Current(ctx, a.City, a.CountryCode)
		if err != nil {
			return "", true, errors.Wrap(err, "get weather")
		}

		desc := a.City
		if info.Description != "" {
			desc = info.Description
		}

		weatherInfo := fmt.Sprintf(
			"Погода в %s (%s): %s, %d °C, ощущается как %d °C, влажность %d%%, ветер %d м/с %s",
			info.LocationName,
			info.Country,
			desc,
			info.Temperature,
			info.FeelsLike,
			info.Humidity,
			info.WindSpeed,
			info.WindDir,
		)

		lg.Info("Adding weather info to dialog", zap.String("weather_info", weatherInfo))

		return weatherInfo, true, nil

	case "get_discord_channels":
		channels, err := t.discord.PopulatedChannels(ctx)
		if err != nil {
			return "", true, errors.Wrap(err, "get discord channels")
		}

		payload, err := json.Marshal(channels)
		if err != nil {
			return "", true, errors.Wrap(err, "marshal discord channels")
		}

		lg.Info("get_discord_channels result",
			zap.Int("channels", len(channels)),
			zap.Any("discord_channels", channels),
			zap.String("payload", string(payload)),
		)

		return string(payload), true, nil

	case "generate_image":
		var a struct {
			Prompt       string `json:"prompt"`
			PositiveTags string `json:"positive_tags"`
			NegativeTags string `json:"negative_tags"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", true, errors.Wrap(err, "unmarshal arguments")
		}

		lg.Info("Generate image",
			zap.String("positive", a.PositiveTags),
			zap.String("negative", a.NegativeTags),
			zap.String("prompt", a.Prompt),
			zap.Bool("reference", req.ImageURL != ""),
		)

		// Keep the "sending photo" indicator alive for the duration of
		// generation, which the typing keepalive does not cover.
		stopPresence := keepAlivePresence(ctx, lg, req.UploadingPhoto)

		// Primary: natural-language generation, with the current message's image
		// (if any) as the image-to-image reference.
		images, err := t.image.Generate(ctx, lilith.ImageRequest{
			Prompt:         a.Prompt,
			ReferenceImage: req.ImageURL,
		})
		if err != nil {
			lg.Warn("Primary image generation failed", zap.Error(err))
		}

		// Fallback: when the primary produces nothing, retry with the tag-based
		// generator using the booru-style tags.
		if len(images) == 0 && t.imageFallback != nil {
			lg.Info("Falling back to secondary image generator")

			fallbackPositive := "very aesthetic, masterpiece, no text"
			if a.PositiveTags != "" {
				fallbackPositive = fallbackPositive + ", " + a.PositiveTags
			}

			const fallbackNegative = "lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blank page"
			fallbackNeg := fallbackNegative
			if a.NegativeTags != "" {
				fallbackNeg = fallbackNeg + ", " + a.NegativeTags
			}

			images, err = t.imageFallback.Generate(ctx, lilith.ImageRequest{
				Prompt:         fallbackPositive,
				NegativePrompt: fallbackNeg,
				ReferenceImage: req.ImageURL,
			})
			if err != nil {
				lg.Warn("Fallback image generation failed", zap.Error(err))
			}
		}

		stopPresence()

		result.Images = append(result.Images, images...)
		if len(images) > 0 {
			// Persist the full tool arguments (prompt + tags) as JSON so the
			// model can recall and reuse them for re-generation.
			if promptJSON, err := json.Marshal(a); err == nil {
				result.ImagePrompt = string(promptJSON)
			} else {
				result.ImagePrompt = a.Prompt
			}
		}

		lg.Info("generate_image result", zap.Int("images", len(images)))

		payload, err := json.Marshal(struct {
			Generated bool `json:"generated"`
			Count     int  `json:"count"`
		}{Generated: len(images) > 0, Count: len(images)})
		if err != nil {
			return "", true, errors.Wrap(err, "marshal image result")
		}

		return string(payload), true, nil

	default:
		lg.Warn("Unknown function call", zap.String("name", name))

		return "", false, nil
	}
}
