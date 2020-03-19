// Copyright 2020 Google LLC
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

package shared

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"strings"

	"github.com/apigee/apigee-remote-service-cli/apigee"
	"gopkg.in/yaml.v2"
)

const (
	// GCPExperienceBase is the default management API URL for GCP Experience
	GCPExperienceBase = "https://apigee.googleapis.com"

	// LegacySaaSManagementBase is the default base for legacy SaaS management operations
	LegacySaaSManagementBase = "https://api.enterprise.apigee.com"

	// DefaultManagementBase is the base URL for GCE Experience management operations
	DefaultManagementBase = GCPExperienceBase

	// RuntimeBaseFormat is a format for base of the organization runtime URL (legacy SaaS and OPDK)
	RuntimeBaseFormat = "https://%s-%s.apigee.net"

	internalProxyURLFormat      = "%s://istioservices.%s/edgemicro" // runtime scheme, runtime domain (legacy SaaS and OPDK)
	internalProxyURLFormatOPDK  = "%s/edgemicro"                    // runtimeBase
	remoteServiceProxyURLFormat = "%s/remote-service"               // runtimeBase
)

// BuildInfoType holds version information
type BuildInfoType struct {
	Version string
	Commit  string
	Date    string
}

// BuildInfo is populated by main init()
var BuildInfo BuildInfoType

// RootArgs is the base struct to hold all command arguments
type RootArgs struct {
	RuntimeBase        string // "https://org-env.apigee.net"
	ManagementBase     string // "https://api.enterprise.apigee.com"
	Verbose            bool
	Org                string
	Env                string
	Username           string
	Password           string
	Token              string
	NetrcPath          string
	IsOPDK             bool
	IsLegacySaaS       bool
	IsGCPManaged       bool
	OverrideConfigFile string

	// the following is derived in Resolve()
	InternalProxyURL      string
	RemoteServiceProxyURL string
	Client                *apigee.EdgeClient
	ClientOpts            *apigee.EdgeClientOptions
}

// Resolve is used to populate shared args, it's automatically called prior when creating the root command
func (r *RootArgs) Resolve(skipAuth bool) error {
	if r.IsLegacySaaS && r.IsOPDK {
		return errors.New("--legacy and --opdk options are exclusive")
	}
	r.IsGCPManaged = !(r.IsLegacySaaS || r.IsOPDK)

	if r.IsGCPManaged {
		err := r.loadOverrideConfig()
		if err != nil {
			return fmt.Errorf("--override-config-file %s load failed: %s", r.OverrideConfigFile, err)
		}
	}

	if r.Org == "" || r.Env == "" {
		return fmt.Errorf("--environment and --organization are required")
	}

	if r.IsLegacySaaS && r.ManagementBase == DefaultManagementBase {
		r.ManagementBase = LegacySaaSManagementBase
	}

	if r.RuntimeBase == "" {
		if !r.IsLegacySaaS {
			return errors.New("--runtime is required")
		}
		r.RuntimeBase = fmt.Sprintf(RuntimeBaseFormat, r.Org, r.Env)
	}

	// calculate internal proxy URL from runtime URL for LegacySaaS or OPDK
	// note: GCPExperience doesn't have an internal proxy
	if r.IsOPDK {
		r.InternalProxyURL = fmt.Sprintf(internalProxyURLFormatOPDK, r.RuntimeBase)
	}
	if r.IsLegacySaaS {
		u, err := url.Parse(r.RuntimeBase)
		if err != nil {
			return err
		}
		domain := u.Host[strings.Index(u.Host, ".")+1:]
		r.InternalProxyURL = fmt.Sprintf(internalProxyURLFormat, u.Scheme, domain)
	}

	r.RemoteServiceProxyURL = fmt.Sprintf(remoteServiceProxyURLFormat, r.RuntimeBase)

	r.ClientOpts = &apigee.EdgeClientOptions{
		MgmtURL: r.ManagementBase,
		Org:     r.Org,
		Env:     r.Env,
		Auth: &apigee.EdgeAuth{
			NetrcPath:   r.NetrcPath,
			Username:    r.Username,
			Password:    r.Password,
			BearerToken: r.Token,
			SkipAuth:    skipAuth,
		},
		GCPManaged: r.IsGCPManaged,
		Debug:      r.Verbose,
	}

	var err error
	r.Client, err = apigee.NewEdgeClient(r.ClientOpts)
	if err != nil {
		if strings.Contains(err.Error(), ".netrc") { // no .netrc and no auth
			baseURL, err := url.Parse(r.ManagementBase)
			if err != nil {
				return fmt.Errorf("unable to parse managementBase url %s: %v", r.ManagementBase, err)
			}
			return fmt.Errorf("no auth: must have username and password or a ~/.netrc entry for %s", baseURL.Host)
		}
		return fmt.Errorf("error initializing Edge client: %v", err)
	}

	return nil
}

// FormatFn formats the supplied arguments according to the format string
// provided and executes some set of operations with the result.
type FormatFn func(format string, args ...interface{})

// Fatalf is a FormatFn that prints the formatted string to os.Stderr and then
// calls os.Exit().
func Fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(-1)
}

// Printf is a FormatFn that prints the formatted string to os.Stdout.
func Printf(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

// Errorf is a FormatFn that prints the formatted string to os.Stderr.
func Errorf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// NoPrintf is a FormatFn that does nothing
func NoPrintf(format string, args ...interface{}) {
}

type overrideConfig struct {
	Org  string        `yaml:"org"`
	Envs []OverrideEnv `yaml:"envs"`
}

// OverrideEnv is subconfig of overrideConfig
type OverrideEnv struct {
	Name      string `yaml:"name"`
	HostAlias string `yaml:"hostAlias"`
}

func (r *RootArgs) loadOverrideConfig() error {
	if r.OverrideConfigFile == "" {
		return nil
	}
	c := &overrideConfig{}
	yamlFile, err := ioutil.ReadFile(r.OverrideConfigFile)
	if err == nil {
		err = yaml.Unmarshal(yamlFile, c)
	}
	if err != nil {
		return err
	}

	if r.Org == "" {
		r.Org = c.Org
	}

	if len(c.Envs) == 1 {
		loadEnv(r, c.Envs[0])
	} else {
		for _, e := range c.Envs {
			loadEnv(r, e)
			break
		}
	}

	if r.Env == "" {
		return fmt.Errorf("--override-config-file %s has multiple envs, --env required", r.OverrideConfigFile)
	}

	return nil
}

func loadEnv(r *RootArgs, env OverrideEnv) {
	if r.Env == "" {
		r.Env = env.Name
	}
	if r.RuntimeBase == "" {
		r.RuntimeBase = fmt.Sprintf("https://%s", env.HostAlias)
	}
}
