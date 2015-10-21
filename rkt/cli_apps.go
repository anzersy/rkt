// Copyright 2015 The rkt Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//+build linux

package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/coreos/rkt/common/apps"

	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/appc/spec/schema"
	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/appc/spec/schema/types"
	"github.com/coreos/rkt/Godeps/_workspace/src/github.com/spf13/pflag"
)

var (
	rktApps apps.Apps // global used by run/prepare for representing the apps expressed via the cli
)

// parseApps looks through the args for support of per-app argument lists delimited with "--" and "---".
// Between per-app argument lists flags.Parse() is called using the supplied FlagSet.
// Anything not consumed by flags.Parse() and not found to be a per-app argument list is treated as an image.
// allowAppArgs controls whether "--" prefixed per-app arguments will be accepted or not.
func parseApps(al *apps.Apps, args []string, flags *pflag.FlagSet, allowAppArgs bool) error {
	nAppsLastAppArgs := al.Count()

	// valid args here may either be:
	// not-"--"; flags handled by *flags or an image specifier
	// "--"; app arguments begin
	// "---"; conclude app arguments
	// between "--" and "---" pairs anything is permitted.
	inAppArgs := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if inAppArgs {
			switch a {
			case "---":
				// conclude this app's args
				inAppArgs = false
			default:
				// keep appending to this app's args
				app := al.Last()
				app.Args = append(app.Args, a)
			}
		} else {
			switch a {
			case "--":
				if !allowAppArgs {
					return fmt.Errorf("app arguments unsupported")
				}
				// begin app's args
				inAppArgs = true

				// catch some likely mistakes
				if nAppsLastAppArgs == al.Count() {
					if al.Count() == 0 {
						return fmt.Errorf("an image is required before any app arguments")
					}
					return fmt.Errorf("only one set of app arguments allowed per image")
				}
				nAppsLastAppArgs = al.Count()
			case "---":
				// ignore triple dashes since they aren't images
				// TODO(vc): I don't think ignoring this is appropriate, probably should error; it implies malformed argv.
				// "---" is not an image separator, it's an optional argument list terminator.
				// encountering it outside of inAppArgs is likely to be "--" typoed
			default:
				// consume any potential inter-app flags
				if err := flags.Parse(args[i:]); err != nil {
					return err
				}
				nInterFlags := (len(args[i:]) - flags.NArg())

				if nInterFlags > 0 {
					// XXX(vc): flag.Parse() annoyingly consumes the "--", reclaim it here if necessary
					if args[i+nInterFlags-1] == "--" {
						nInterFlags--
					}

					// advance past what flags.Parse() consumed
					i += nInterFlags - 1 // - 1 because of i++
				} else {
					// flags.Parse() didn't want this arg, treat as image
					al.Create(a)
				}
			}
		}
	}

	return al.Validate()
}

// Value interface implementations for the various per-app fields we provide flags for

// appAsc is for aci --signature
type appAsc apps.Apps

func (aa *appAsc) Set(s string) error {
	app := (*apps.Apps)(aa).Last()
	if app == nil {
		return fmt.Errorf("--signature must follow an image")
	}
	if app.Asc != "" {
		return fmt.Errorf("--signature specified multiple times for the same image")
	}
	app.Asc = s

	return nil
}

func (aa *appAsc) String() string {
	app := (*apps.Apps)(aa).Last()
	if app == nil {
		return ""
	}
	return app.Asc
}

func (aa *appAsc) Type() string {
	return "appAsc"
}

// appExec is for aci --exec overrides
type appExec apps.Apps

func (ae *appExec) Set(s string) error {
	app := (*apps.Apps)(ae).Last()
	if app == nil {
		return fmt.Errorf("--exec must follow an image")
	}
	if !filepath.IsAbs(s) {
		return fmt.Errorf("--exec must be absolute path")
	}
	if app.Exec != "" {
		return fmt.Errorf("--exec specified multiple times for the same image")
	}
	app.Exec = s

	return nil
}

// appInjectVolume is for --inject-volume flags in the form of: --inject-volume source=PATH,target=PATH
type appInjectVolume apps.Apps

func (aam *appInjectVolume) Set(s string) error {
	app := (*apps.Apps)(aam).Last()
	if app == nil {
		return fmt.Errorf("--inject-volume must follow an image")
	}

	p := strings.SplitN(s, ":", 2)
	if len(p) != 2 {
		return fmt.Errorf("must be SOURCE_PATH:TARGET_PATH")
	}

	volName, err := types.SanitizeACName(p[1])
	if err != nil {
		return fmt.Errorf("empty target in volume")
	}

	if volName != p[1] {
		stderr("Changed volume target %q to %q", p[1], volName)
	}

	rVol := types.Volume{Name: *types.MustACName(volName), Source: p[0], Kind: "host"}
	mount := schema.Mount{Volume: rVol.Name, Path: p[1]}

	(*apps.Apps)(aam).Volumes = append((*apps.Apps)(aam).Volumes, rVol)
	app.Mounts = append(app.Mounts, mount)

	return nil
}

func (aam *appInjectVolume) Type() string {
	return "appInjectVolume"
}

func (aam *appInjectVolume) String() string {
	return ""
}

// appMount is for --mount flags in the form of: --mount volume=VOLNAME,target=PATH
type appMount apps.Apps

func (al *appMount) Set(s string) error {
	mount := schema.Mount{}

	// this is intentionally made similar to types.VolumeFromString()
	// TODO(iaguis) use MakeQueryString() when appc/spec#520 is merged
	m, err := url.ParseQuery(strings.Replace(s, ",", "&", -1))
	if err != nil {
		return err
	}

	for key, val := range m {
		if len(val) > 1 {
			return fmt.Errorf("label %s with multiple values %q", key, val)
		}
		switch key {
		case "volume":
			mv, err := types.NewACName(val[0])
			if err != nil {
				return fmt.Errorf("invalid volume name %q in --mount flag %q: %v", val[0], s, err)
			}
			mount.Volume = *mv
		case "target":
			mount.Path = val[0]
		default:
			return fmt.Errorf("unknown mount parameter %q", key)
		}
	}

	as := (*apps.Apps)(al)
	if as.Count() == 0 {
		as.Mounts = append(as.Mounts, mount)
	} else {
		app := as.Last()
		app.Mounts = append(app.Mounts, mount)
	}

	return nil
}

func (ae *appExec) String() string {
	app := (*apps.Apps)(ae).Last()
	if app == nil {
		return ""
	}
	return app.Exec
}

func (ae *appExec) Type() string {
	return "appExec"
}

// TODO(vc): --set-env should also be per-app and should implement the flags.Value interface.
func (al *appMount) String() string {
	var ms []string
	for _, m := range ((*apps.Apps)(al)).Mounts {
		ms = append(ms, m.Volume.String(), ":", m.Path)
	}
	return strings.Join(ms, " ")
}

func (al *appMount) Type() string {
	return "appMount"
}

// appsVolume is for --volume flags in the form name,kind=host,source=/tmp,readOnly=true (defined by appc)
type appsVolume apps.Apps

func (al *appsVolume) Set(s string) error {
	vol, err := types.VolumeFromString(s)
	if err != nil {
		return fmt.Errorf("invalid value in --volume flag %q: %v", s, err)
	}

	(*apps.Apps)(al).Volumes = append((*apps.Apps)(al).Volumes, *vol)
	return nil
}

func (al *appsVolume) Type() string {
	return "appsVolume"
}

func (al *appsVolume) String() string {
	var vs []string
	for _, v := range (*apps.Apps)(al).Volumes {
		vs = append(vs, v.String())
	}
	return strings.Join(vs, " ")
}
