/*
Copyright (c) 2020 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This file contains the types and functions used to manage the configuration of the command line
// client.

package ocm

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/golang/glog"
	"github.com/mitchellh/go-homedir"
	sdk "github.com/openshift-online/ocm-sdk-go"

	"github.com/openshift/rosa/pkg/debug"
)

// URLAliases allows the value of the `--env` option to map to the various API URLs.
var URLAliases = map[string]string{
	"production":  "https://api.openshift.com",
	"staging":     "https://api.stage.openshift.com",
	"integration": "https://api.integration.openshift.com",
}

// Config is the type used to store the configuration of the client.
type Config struct {
	AccessToken  string   `json:"access_token,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Insecure     bool     `json:"insecure,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	TokenURL     string   `json:"token_url,omitempty"`
	URL          string   `json:"url,omitempty"`
}

func GetEnv() (string, error) {
	cfg, err := Load()
	if err != nil {
		return "", err
	}

	for env, api := range URLAliases {
		if api == cfg.URL {
			return env, nil
		}
	}

	return "", fmt.Errorf("Invalid OCM API")
}

// Load loads the configuration from the configuration file. If the configuration file doesn't exist
// it will return an empty configuration object.
func Load() (cfg *Config, err error) {
	file, err := Location()
	if err != nil {
		return
	}
	_, err = os.Stat(file)
	if os.IsNotExist(err) {
		cfg = nil
		err = nil
		return
	}
	if err != nil {
		err = fmt.Errorf("Failed to check if config file '%s' exists: %v", file, err)
		return
	}
	// #nosec G304
	data, err := ioutil.ReadFile(file)
	if err != nil {
		err = fmt.Errorf("Failed to read config file '%s': %v", file, err)
		return
	}
	cfg = new(Config)
	err = json.Unmarshal(data, cfg)
	if err != nil {
		err = fmt.Errorf("Failed to parse config file '%s': %v", file, err)
		return
	}
	return
}

// Save saves the given configuration to the configuration file.
func Save(cfg *Config) error {
	file, err := Location()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("Failed to marshal config: %v", err)
	}
	err = ioutil.WriteFile(file, data, 0600)
	if err != nil {
		return fmt.Errorf("Failed to write file '%s': %v", file, err)
	}
	return nil
}

// Remove removes the configuration file.
func Remove() error {
	file, err := Location()
	if err != nil {
		return err
	}
	_, err = os.Stat(file)
	if os.IsNotExist(err) {
		return nil
	}
	err = os.Remove(file)
	if err != nil {
		return err
	}
	return nil
}

// Location returns the location of the configuration file.
func Location() (path string, err error) {
	if ocmconfig := os.Getenv("OCM_CONFIG"); ocmconfig != "" {
		path = ocmconfig
	} else {
		home, err := homedir.Dir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, ".ocm.json")
	}
	return path, nil
}

func (c *Config) GetData(key string) (value string, err error) {
	if c.AccessToken == "" {
		return
	}

	parser := new(jwt.Parser)
	token, _, err := parser.ParseUnverified(c.AccessToken, jwt.MapClaims{})
	if err != nil {
		err = fmt.Errorf("Failed to parse token: %v", err)
		return
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		err = fmt.Errorf("Expected map claims but got %T", claims)
		return
	}
	claim, ok := claims[key]
	if !ok {
		err = fmt.Errorf("Token does not contain the '%s' claim", key)
		return
	}
	value, ok = claim.(string)
	if !ok {
		err = fmt.Errorf("Expected string '%s' but got %T", key, claim)
		return
	}

	return
}

// Armed checks if the configuration contains either credentials or tokens that haven't expired, so
// that it can be used to perform authenticated requests.
func (c *Config) Armed() (armed bool, err error) {
	if c.ClientID != "" && c.ClientSecret != "" {
		armed = true
		return
	}
	now := time.Now()
	if c.AccessToken != "" {
		var expires bool
		var left time.Duration
		var accessToken *jwt.Token
		accessToken, err = parseToken(c.AccessToken)
		if err != nil {
			return
		}
		expires, left, err = getTokenExpiry(accessToken, now)
		if err != nil {
			return
		}
		if !expires || left > 5*time.Second {
			armed = true
			return
		}
	}
	if c.RefreshToken != "" {
		var expires bool
		var left time.Duration
		var refreshToken *jwt.Token
		refreshToken, err = parseToken(c.RefreshToken)
		if err != nil {
			return
		}
		expires, left, err = getTokenExpiry(refreshToken, now)
		if err != nil {
			return
		}
		if !expires || left > 10*time.Second {
			armed = true
			return
		}
	}
	return
}

// GetTokenExpiry determines if the given token expires, and the time that remains till it expires.
func getTokenExpiry(token *jwt.Token, now time.Time) (expires bool, left time.Duration, err error) {
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		err = fmt.Errorf("expected map claims bug got %T", claims)
		return
	}
	var exp float64
	claim, ok := claims["exp"]
	if ok {
		exp, ok = claim.(float64)
		if !ok {
			err = fmt.Errorf("expected floating point 'exp' but got %T", claim)
			return
		}
	}
	if exp == 0 {
		expires = false
		left = 0
	} else {
		expires = true
		left = time.Unix(int64(exp), 0).Sub(now)
	}
	return
}

// Connection creates a connection using this configuration.
func (c *Config) Connection() (connection *sdk.Connection, err error) {
	// Create the logger:
	level := glog.Level(1)
	if debug.Enabled() {
		level = glog.Level(0)
	}
	logger, err := sdk.NewGlogLoggerBuilder().
		DebugV(level).
		InfoV(level).
		WarnV(level).
		Build()
	if err != nil {
		return
	}

	// Prepare the builder for the connection adding only the properties that have explicit
	// values in the configuration, so that default values won't be overridden:
	builder := sdk.NewConnectionBuilder()
	builder.Logger(logger)
	if c.TokenURL != "" {
		builder.TokenURL(c.TokenURL)
	}
	if c.ClientID != "" || c.ClientSecret != "" {
		builder.Client(c.ClientID, c.ClientSecret)
	}
	if c.Scopes != nil {
		builder.Scopes(c.Scopes...)
	}
	if c.URL != "" {
		builder.URL(c.URL)
	}
	tokens := make([]string, 0, 2)
	if c.AccessToken != "" {
		tokens = append(tokens, c.AccessToken)
	}
	if c.RefreshToken != "" {
		tokens = append(tokens, c.RefreshToken)
	}
	if len(tokens) > 0 {
		builder.Tokens(tokens...)
	}
	builder.Insecure(c.Insecure)

	// Create the connection:
	connection, err = builder.Build()
	if err != nil {
		return
	}

	return
}

func parseToken(textToken string) (token *jwt.Token, err error) {
	parser := new(jwt.Parser)
	token, _, err = parser.ParseUnverified(textToken, jwt.MapClaims{})
	if err != nil {
		err = fmt.Errorf("Failed to parse token: %v", err)
		return
	}
	return token, nil
}
