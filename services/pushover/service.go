package pushover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/influxdata/kapacitor/alert"
)

type Service struct {
	configValue atomic.Value
	logger      *log.Logger
}

func NewService(c Config, l *log.Logger) *Service {
	s := &Service{
		logger: l,
	}
	s.configValue.Store(c)
	return s
}

func (s *Service) Open() error {
	return nil
}

func (s *Service) Close() error {
	return nil
}

func (s *Service) Update(newConfig []interface{}) error {
	if l := len(newConfig); l != 1 {
		return fmt.Errorf("expected only one new config object, got %d", l)
	}
	if c, ok := newConfig[0].(Config); !ok {
		return fmt.Errorf("expected config object to be of type %T, got %T", c, newConfig[0])
	} else {
		s.configValue.Store(c)
	}
	return nil
}

type testOptions struct {
	User      string      `json:"user"`
	Message   string      `json:"message"`
	Device    string      `json:"device"`
	Title     string      `json:"title"`
	URL       string      `json:"url"`
	URLTitle  string      `json:"url_title"`
	Sound     string      `json:"sound"`
	Timestamp bool        `json:"timestamp"`
	Level     alert.Level `json:"level"`
}

func (s *Service) TestOptions() interface{} {
	c := s.config()
	return &testOptions{
		User:    c.User,
		Message: "test pushover message",
		Level:   alert.Critical,
	}
}

func (s *Service) Test(options interface{}) error {
	o, ok := options.(*testOptions)
	if !ok {
		return fmt.Errorf("unexpected options type %t", options)
	}

	return s.Alert(
		o.User,
		o.Message,
		o.Device,
		o.Title,
		o.URL,
		o.URLTitle,
		o.Sound,
		o.Timestamp,
		o.Level,
	)
}

func (s *Service) config() Config {
	return s.configValue.Load().(Config)
}

func (s *Service) Alert(user, message, device, title, URL, URLTitle, sound string, timestamp bool, level alert.Level) error {
	url, post, err := s.preparePost(user, message, device, title, URL, URLTitle, sound, timestamp, level)
	if err != nil {
		return err
	}

	resp, err := http.PostForm(url, post)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		type response struct {
			Error string `json:"error"`
		}
		r := &response{Error: fmt.Sprintf("failed to understand Slack response. code: %d content: %s", resp.StatusCode, string(body))}
		b := bytes.NewReader(body)
		dec := json.NewDecoder(b)
		dec.Decode(r)
		return errors.New(r.Error)
	}

	return nil
}

// priority returns the pushover priority as defined by the Pushover API
// documentation https://pushover.net/api
func priority(level alert.Level) int {
	switch level {
	case alert.OK:
		// send as -2 to generate no notification/alert
		return -2
	case alert.Info:
		// -1 to always send as a quiet notification
		return -1
	case alert.Warning:
		// 1 to display as high-priority and bypass the user's quiet hours,
		return 1
	case alert.Critical:
		// 2 to also require confirmation from the user
		return 2
	}

	return 0
}

type postData struct {
	Token     string
	User      string
	Message   string
	Device    string
	Title     string
	URL       string
	URLTitle  string
	Priority  int
	Timestamp *time.Time
	Sound     string
}

func (p *postData) Values() url.Values {
	v := url.Values{}

	v.Set("token", p.Token)
	v.Set("user", p.User)
	v.Set("message", p.Message)
	v.Set("priority", strconv.Itoa(p.Priority))

	if p.Device != "" {
		v.Set("device", p.Device)
	}

	if p.Title != "" {
		v.Set("title", p.Title)
	}

	if p.URL != "" {
		v.Set("url", p.URL)
	}

	if p.URLTitle != "" {
		v.Set("url_title", p.URLTitle)
	}

	if p.Sound != "" {
		v.Set("sound", p.Sound)
	}

	if p.Timestamp != nil {
		v.Set("timestamp", p.Timestamp.String())
	}

	return v

}

func (s *Service) preparePost(user, message, device, title, URL, URLTitle, sound string, timestamp bool, level alert.Level) (string, url.Values, error) {
	c := s.config()

	if !c.Enabled {
		return "", nil, errors.New("service is not enabled")
	}

	p := postData{
		Token:   c.Token,
		User:    c.User,
		Message: message,
	}

	if user != "" {
		p.User = user
	}

	p.Device = device
	p.Title = title
	p.URL = URL
	p.URLTitle = URLTitle
	p.Sound = sound

	if timestamp {
		now := time.Now()
		p.Timestamp = &now
	}

	p.Priority = priority(level)

	return c.URL, p.Values(), nil
}

type HandlerConfig struct {
	// User/Group key of your user (or you), viewable when logged
	// into the Pushover dashboard. Often referred to as USER_KEY
	// in the Pushover documentation.
	// If empty uses the user from the configuration.
	User string `mapstructure:"user"`

	// Users device name to send message directly to that device,
	// rather than all of a user's devices (multiple device names may
	// be separated by a comma)
	Device string `mapstructure:"device"`

	// Your message's title, otherwise your apps name is used
	Title string `mapstructure:"title"`

	// A supplementary URL to show with your message
	URL string `mapstructure:"url"`

	// A title for your supplementary URL, otherwise just URL is shown
	URLTitle string `mapstructure:"url_title"`

	// The name of one of the sounds supported by the device clients to override
	// the user's default sound choice
	Sound string `mapstructure:"sound"`

	// A Unix timestamp of your message's date and time to display to the user,
	// rather than the time your message is received by the Pushover API
	Timestamp bool `mapstructure:"timestamp"`
}

type handler struct {
	s      *Service
	c      HandlerConfig
	logger *log.Logger
}

func (s *Service) DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{}
}

func (s *Service) Handler(c HandlerConfig, l *log.Logger) alert.Handler {
	return &handler{
		s:      s,
		c:      c,
		logger: l,
	}
}

func (h *handler) Handle(event alert.Event) {
	if err := h.s.Alert(
		h.c.User,
		event.State.Message,
		h.c.Device,
		h.c.Title,
		h.c.URL,
		h.c.URLTitle,
		h.c.Sound,
		h.c.Timestamp,
		event.State.Level,
	); err != nil {
		h.logger.Println("E! failed to send event to Pushover", err)
	}
}
