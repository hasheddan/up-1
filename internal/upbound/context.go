// Copyright 2021 Upbound Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upbound

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"

	"github.com/alecthomas/kong"
	"github.com/pkg/errors"
	"github.com/spf13/afero"

	"github.com/upbound/up-sdk-go"
	"github.com/upbound/up/internal/config"
)

const (
	// UserAgent is the default user agent to use to make requests to the
	// Upbound API.
	UserAgent = "up-cli"
	// CookieName is the default cookie name used to identify a session token.
	CookieName = "SID"

	// Default API subdomain.
	apiSubdomain = "api."
	// Default proxy subdomain.
	proxySubdomain = "proxy."

	// Base path for proxy.
	proxyPath = "/v1/controlPlanes"

	// Default registry subdomain.
	xpkgSubdomain = "xpkg."
)

const (
	errProfileNotFoundFmt = "profile not found with identifier: %s"
)

// Flags are common flags used by commands that interact with Upbound.
type Flags struct {
	// Optional
	Domain  *url.URL `env:"UP_DOMAIN" default:"https://upbound.io" help:"Root Upbound domain."`
	Profile string   `env:"UP_PROFILE" help:"Profile used to execute command."`
	Account string   `short:"a" env:"UP_ACCOUNT" help:"Account used to execute command."`

	// Insecure
	InsecureSkipTLSVerify bool `env:"UP_INSECURE_SKIP_TLS_VERIFY" help:"[INSECURE] Skip verifying TLS certificates."`

	// Hidden
	APIEndpoint      *url.URL `env:"OVERRIDE_API_ENDPOINT" hidden:"" name:"override-api-endpoint" help:"Overrides the default API endpoint."`
	ProxyEndpoint    *url.URL `env:"OVERRIDE_PROXY_ENDPOINT" hidden:"" name:"override-proxy-endpoint" help:"Overrides the default proxy endpoint."`
	RegistryEndpoint *url.URL `env:"OVERRIDE_REGISTRY_ENDPOINT" hidden:"" name:"override-registry-endpoint" help:"Overrides the default registry endpoint."`
}

// Context includes common data that Upbound consumers may utilize.
type Context struct {
	ProfileName string
	Profile     config.Profile
	Token       string
	Account     string
	Domain      *url.URL

	InsecureSkipTLSVerify bool

	APIEndpoint      *url.URL
	ProxyEndpoint    *url.URL
	RegistryEndpoint *url.URL
	Cfg              *config.Config
	CfgSrc           config.Source

	allowMissingProfile bool
	cfgPath             string
	fs                  afero.Fs
}

// Option modifies a Context
type Option func(*Context)

// AllowMissingProfile indicates that Context should still be returned even if a
// profile name is supplied and it does not exist in config.
func AllowMissingProfile() Option {
	return func(ctx *Context) {
		ctx.allowMissingProfile = true
	}
}

// NewFromFlags constructs a new context from flags.
func NewFromFlags(f Flags, opts ...Option) (*Context, error) { //nolint:gocyclo
	p, err := config.GetDefaultPath()
	if err != nil {
		return nil, err
	}

	c := &Context{
		fs:      afero.NewOsFs(),
		cfgPath: p,
	}

	for _, o := range opts {
		o(c)
	}

	src := config.NewFSSource(
		config.WithFS(c.fs),
		config.WithPath(c.cfgPath),
	)
	if err := src.Initialize(); err != nil {
		return nil, err
	}
	conf, err := config.Extract(src)
	if err != nil {
		return nil, err
	}

	c.Cfg = conf
	c.CfgSrc = src

	// If profile identifier is not provided, use the default, or empty if the
	// default cannot be obtained.
	c.Profile = config.Profile{}
	if f.Profile == "" {
		if name, p, err := c.Cfg.GetDefaultUpboundProfile(); err == nil {
			c.Profile = p
			c.ProfileName = name
		}
	} else {
		p, err := c.Cfg.GetUpboundProfile(f.Profile)
		if err != nil && !c.allowMissingProfile {
			return nil, errors.Errorf(errProfileNotFoundFmt, f.Profile)
		}
		c.Profile = p
		c.ProfileName = f.Profile
	}

	of, err := c.applyOverrides(f, c.ProfileName)
	if err != nil {
		return nil, err
	}

	c.APIEndpoint = of.APIEndpoint
	if c.APIEndpoint == nil {
		u := *of.Domain
		u.Host = apiSubdomain + u.Host
		c.APIEndpoint = &u
	}

	c.ProxyEndpoint = of.ProxyEndpoint
	if c.ProxyEndpoint == nil {
		u := *of.Domain
		u.Host = proxySubdomain + u.Host
		u.Path = proxyPath
		c.ProxyEndpoint = &u
	}

	c.RegistryEndpoint = of.RegistryEndpoint
	if c.RegistryEndpoint == nil {
		u := *of.Domain
		u.Host = xpkgSubdomain + u.Host
		c.RegistryEndpoint = &u
	}

	c.Account = of.Account
	c.Domain = of.Domain

	// If account has not already been set, use the profile default.
	if c.Account == "" {
		c.Account = c.Profile.Account
	}

	c.InsecureSkipTLSVerify = of.InsecureSkipTLSVerify

	return c, nil
}

// BuildSDKConfig builds an Upbound SDK config suitable for usage with any
// service client.
func (c *Context) BuildSDKConfig(session string) (*up.Config, error) {
	cj, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	cj.SetCookies(c.APIEndpoint, []*http.Cookie{{
		Name:  CookieName,
		Value: session,
	},
	})
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: c.InsecureSkipTLSVerify, //nolint:gosec
		},
	}
	client := up.NewClient(func(u *up.HTTPClient) {
		u.BaseURL = c.APIEndpoint
		u.HTTP = &http.Client{
			Jar:       cj,
			Transport: tr,
		}
		u.UserAgent = UserAgent
	})
	return up.NewConfig(func(conf *up.Config) {
		conf.Client = client
	}), nil
}

// applyOverrides applies applicable overrides to the given Flags based on the
// pre-existing configs, if there are any.
func (c *Context) applyOverrides(f Flags, profileName string) (Flags, error) {
	// profile doesn't exist, return the supplied flags
	if _, ok := c.Cfg.Upbound.Profiles[profileName]; !ok {
		return f, nil
	}

	of := Flags{}

	baseReader, err := c.Cfg.BaseToJSON(profileName)
	if err != nil {
		return of, err
	}

	overlayBytes, err := json.Marshal(f)
	if err != nil {
		return of, err
	}

	resolver, err := JSON(baseReader, bytes.NewReader(overlayBytes))
	if err != nil {
		return of, err
	}
	parser, err := kong.New(&of, kong.Resolvers(resolver))
	if err != nil {
		return of, err
	}

	if _, err = parser.Parse([]string{}); err != nil {
		return of, err
	}

	return of, nil
}

// MarshalJSON marshals the Flags struct, converting the url.URL to strings.
func (f Flags) MarshalJSON() ([]byte, error) {
	flags := struct {
		Domain                string `json:"domain,omitempty"`
		Profile               string `json:"profile,omitempty"`
		Account               string `json:"account,omitempty"`
		InsecureSkipTLSVerify bool   `json:"insecure_skip_tls_verify,omitempty"`
		APIEndpoint           string `json:"override_api_endpoint,omitempty"`
		ProxyEndpoint         string `json:"override_proxy_endpoint,omitempty"`
		RegistryEndpoint      string `json:"override_registry_endpoint,omitempty"`
	}{
		Domain:                nullableURL(f.Domain),
		Profile:               f.Profile,
		Account:               f.Account,
		InsecureSkipTLSVerify: f.InsecureSkipTLSVerify,
		APIEndpoint:           nullableURL(f.APIEndpoint),
		ProxyEndpoint:         nullableURL(f.ProxyEndpoint),
		RegistryEndpoint:      nullableURL(f.RegistryEndpoint),
	}
	return json.Marshal(flags)
}

func nullableURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
