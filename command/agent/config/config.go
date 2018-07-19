package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/errwrap"
	log "github.com/hashicorp/go-hclog"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/hashicorp/vault/helper/parseutil"

	"github.com/hashicorp/hcl"
	"github.com/hashicorp/hcl/hcl/ast"
)

// Config is the configuration for the vault server.
type Config struct {
	AutoAuth *AutoAuth `hcl:"auto_auth"`
	PidFile  string    `hcl:"pid_file"`
}

type AutoAuth struct {
	Method *Method `hcl:"-"`
	Sinks  []*Sink `hcl:"sinks"`
}

type Method struct {
	Type      string
	MountPath string `hcl:"mount_path"`
	Config    map[string]interface{}
}

type Sink struct {
	Type       string
	WrapTTLRaw interface{}   `hcl:"wrap_ttl"`
	WrapTTL    time.Duration `hcl:"-"`
	DHType     string        `hcl:"dh_type"`
	DHPath     string        `hcl:"dh_path"`
	AAD        string        `hcl:"aad"`
	AADEnvVar  string        `hcl:"aad_env_var"`
	Config     map[string]interface{}
}

// LoadConfig loads the configuration at the given path, regardless if
// its a file or directory.
func LoadConfig(path string, logger log.Logger) (*Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if fi.IsDir() {
		return nil, fmt.Errorf("location is a directory, not a file")
	}

	// Read the file
	d, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Parse!
	obj, err := hcl.Parse(string(d))
	if err != nil {
		return nil, err
	}

	// Start building the result
	var result Config
	if err := hcl.DecodeObject(&result, obj); err != nil {
		return nil, err
	}

	list, ok := obj.Node.(*ast.ObjectList)
	if !ok {
		return nil, fmt.Errorf("error parsing: file doesn't contain a root object")
	}

	if err := parseAutoAuth(&result, list); err != nil {
		return nil, errwrap.Wrapf("error parsing 'auto_auth': {{err}}", err)
	}

	return &result, nil
}

func parseAutoAuth(result *Config, list *ast.ObjectList) error {
	name := "auto_auth"

	autoAuthList := list.Filter(name)
	if len(autoAuthList.Items) != 1 {
		return fmt.Errorf("one and only one %q block is required", name)
	}

	// Get our item
	item := autoAuthList.Items[0]

	var a AutoAuth
	if err := hcl.DecodeObject(&a, item.Val); err != nil {
		return err
	}

	result.AutoAuth = &a

	subs, ok := item.Val.(*ast.ObjectType)
	if !ok {
		return fmt.Errorf("could not parse %q as an object", name)
	}
	subList := subs.List

	if err := parseMethod(result, subList); err != nil {
		return errwrap.Wrapf("error parsing 'method': {{err}}", err)
	}

	if err := parseSinks(result, subList); err != nil {
		return errwrap.Wrapf("error parsing 'sink' stanzas: {{err}}", err)
	}

	switch {
	case a.Method == nil:
		return fmt.Errorf("no 'method' block found")
	case len(a.Sinks) == 0:
		return fmt.Errorf("at least one 'sink' block must be provided")
	}

	return nil
}

func parseMethod(result *Config, list *ast.ObjectList) error {
	name := "method"

	methodList := list.Filter(name)
	if len(methodList.Items) != 1 {
		return fmt.Errorf("one and only one %q block is required", name)
	}

	// Get our item
	item := methodList.Items[0]

	var m Method
	if err := hcl.DecodeObject(&m, item.Val); err != nil {
		return err
	}

	// Default to Vault's default
	if m.MountPath == "" {
		m.MountPath = fmt.Sprintf("auth/%s", m.Type)
	}
	// Standardize on no trailing slash
	m.MountPath = strings.TrimSuffix(m.MountPath, "/")

	result.AutoAuth.Method = &m
	return nil
}

func parseSinks(result *Config, list *ast.ObjectList) error {
	name := "sink"

	sinkList := list.Filter(name)
	if len(sinkList.Items) < 1 {
		return fmt.Errorf("at least one %q block is required", name)
	}

	var ts []*Sink

	for _, item := range sinkList.Items {
		if len(item.Keys) == 0 {
			return fmt.Errorf("token sink type must be specified")
		}

		tsType := strings.ToLower(item.Keys[0].Token.Value().(string))

		var m map[string]interface{}
		if err := hcl.DecodeObject(&m, item.Val); err != nil {
			return multierror.Prefix(err, fmt.Sprintf("sink.%s", tsType))
		}

		var wrapTTL time.Duration
		if raw, ok := m["wrap_ttl"]; ok {
			var err error
			if wrapTTL, err = parseutil.ParseDurationSecond(raw); err != nil {
				return multierror.Prefix(err, fmt.Sprintf("sink.%s", tsType))
			}
		}

		var dhType string
		if raw, ok := m["dh_type"]; ok {
			var ok bool
			if dhType, ok = raw.(string); !ok {
				return multierror.Prefix(errors.New("cannot convert 'dh_type' to string"), fmt.Sprintf("sink.%s", tsType))
			}
			switch dhType {
			case "curve25519":
			default:
				return multierror.Prefix(errors.New("invalid value for 'dh_type'"), fmt.Sprintf("sink.%s", tsType))
			}
		}

		var dhPath string
		if raw, ok := m["dh_path"]; ok {
			var ok bool
			if dhPath, ok = raw.(string); !ok {
				return multierror.Prefix(errors.New("cannot convert 'dh_path' to string"), fmt.Sprintf("sink.%s", tsType))
			}
		}

		var aad string
		if raw, ok := m["aad"]; ok {
			var ok bool
			if aad, ok = raw.(string); !ok {
				return multierror.Prefix(errors.New("cannot convert 'aad' to string"), fmt.Sprintf("sink.%s", tsType))
			}
		} else if raw, ok := m["aad_env_var"]; ok {
			var ok bool
			var envvar string
			if envvar, ok = raw.(string); !ok {
				return multierror.Prefix(errors.New("cannot convert 'aad_env_var' to string"), fmt.Sprintf("sink.%s", tsType))
			}
			aad = os.Getenv(envvar)
		}

		switch {
		case dhPath == "" && dhType == "":
			if aad != "" {
				return multierror.Prefix(errors.New("specifying AAD data without 'dh_type' does not make sense"), fmt.Sprintf("sink.%s", tsType))
			}
		case dhPath != "" && dhType != "":
		default:
			return multierror.Prefix(errors.New("'dh_type' and 'dh_path' must be specified together"), fmt.Sprintf("sink.%s", tsType))
		}

		ts = append(ts, &Sink{
			Type:    tsType,
			WrapTTL: wrapTTL,
			DHType:  dhType,
			DHPath:  dhPath,
			AAD:     aad,
			Config:  m,
		})
	}

	result.AutoAuth.Sinks = ts
	return nil
}
