package pushover

import (
	"net/url"

	"github.com/pkg/errors"
)

// DefaultPushoverURL is the default URL for the Pushover API.
const DefaultPushoverURL = "https://api.pushover.net/1/messages.json"

// Config is the [pushover] configuration as defined in the Kapacitor configuration file.
type Config struct {
	// Whether Pushover integration is enabled.
	Enabled bool `toml:"enabled" override:"enabled"`
	// The Pushover API token.
	Token string `toml:"token" override:"token,redact"`
	// The User/Group that will be alerted.
	User string `toml:"user" override:"user"`
	// The URL for the Pushover API.
	// Default: DefaultPushoverAPI
	URL string `toml:"url" override:"url"`
}

// NewConfig returns a new Pushover configuration with the URL set to be
// the default pushover URL.
func NewConfig() Config {
	return Config{
		URL: DefaultPushoverURL,
	}
}

// Validate ensures that all configuration options are valid. The
// Token, User, and URL parameters must be specified to be considered
// valid.
func (c Config) Validate() error {
	if c.Enabled && c.Token == "" {
		return errors.New("must specify token")
	}

	if c.Enabled && c.User == "" {
		return errors.New("must specify user")
	}

	if c.Enabled && c.URL == "" {
		return errors.New("must specify url")
	}

	if _, err := url.Parse(c.URL); err != nil {
		return errors.Wrapf(err, "invalid URL %q", c.URL)
	}

	return nil
}
