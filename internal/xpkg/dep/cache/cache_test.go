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

package cache

import (
	"os"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	apimetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ociname "github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/pkg/test"
	xpmetav1 "github.com/crossplane/crossplane/apis/pkg/meta/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"

	"github.com/upbound/up/internal/xpkg/dep/resolver/xpkg"
)

var (
	providerAws = "crossplane/provider-aws"

	pkg1 = &xpkg.ParsedPackage{
		MetaObj: &xpmetav1.Provider{
			TypeMeta: apimetav1.TypeMeta{
				APIVersion: "meta.pkg.crossplane.io/v1alpha1",
				Kind:       "Provider",
			},
			ObjectMeta: apimetav1.ObjectMeta{
				Name: "provider-aws",
			},
		},
		PType: v1beta1.ProviderPackageType,
		SHA:   "sha256:d507e508234732c6dc95d29c8a8c932fa8fa6a229231e309927641f99933892e",
		Reg:   "index.docker.io/crossplane/provider-aws",
		Ver:   "v0.20.1-alpha",
	}

	pkg2 = &xpkg.ParsedPackage{
		MetaObj: &xpmetav1.Provider{
			TypeMeta: apimetav1.TypeMeta{
				APIVersion: "meta.pkg.crossplane.io/v1alpha1",
				Kind:       "Provider",
			},
			ObjectMeta: apimetav1.ObjectMeta{
				Name: "provider-gcp",
			},
		},
		PType: v1beta1.ProviderPackageType,
		SHA:   "sha256:d507e508234732c6dc95d29c8a8c932fa8fa6a229231e309927077099933707",
		Reg:   "index.docker.io/crossplane/provider-gcp",
		Ver:   "v0.18.1",
	}
)

func TestGet(t *testing.T) {
	fs := afero.NewMemMapFs()

	cache, _ := NewLocal(
		WithFS(fs),
		WithRoot("/cache"),
		// override HomeDirFn
		rootIsHome,
	)

	e := cache.newEntry(pkg1)

	cache.add(e, "index.docker.io/crossplane/provider-aws@v0.20.1-alpha")

	type args struct {
		cache *Local
		key   v1beta1.Dependency
	}

	type want struct {
		err error
		val *xpkg.ParsedPackage
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Should not return an error if package exists at path.",
			args: args{
				cache: cache,
				key: v1beta1.Dependency{
					Package:     providerAws,
					Constraints: "v0.20.1-alpha",
				},
			},
			want: want{
				val: e.pkg,
			},
		},
		// "ErrNotExist": {
		// 	reason: "Should return error if package does not exist at path.",
		// 	args: args{
		// 		cache: cache,
		// 		key: v1beta1.Dependency{
		// 			Package:     providerAws,
		// 			Constraints: "v0.20.1-alpha1",
		// 		},
		// 	},
		// 	want: want{
		// 		err: &os.PathError{Op: "open", Path: "/cache/index.docker.io/crossplane/provider-aws@v0.20.1-alpha1", Err: afero.ErrFileNotFound},
		// 	},
		// },
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v, err := tc.args.cache.Get(tc.args.key)

			if tc.want.val != nil {
				if diff := cmp.Diff(tc.want.val.Digest(), v.Digest()); diff != "" {
					t.Errorf("\n%s\nGet(...): -want err, +got err:\n%s", tc.reason, diff)
				}
			}

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nGet(...): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestStore(t *testing.T) {
	dep1 := v1beta1.Dependency{
		Package:     "crossplane/exist-xpkg",
		Type:        v1beta1.ProviderPackageType,
		Constraints: "latest",
	}

	dep2 := v1beta1.Dependency{
		Package:     "crossplane/dep2-xpkg",
		Type:        v1beta1.ProviderPackageType,
		Constraints: "latest",
	}

	type setup struct {
		dep v1beta1.Dependency
		pkg *xpkg.ParsedPackage
	}

	type args struct {
		cache *Local
		dep   v1beta1.Dependency
		pkg   *xpkg.ParsedPackage
		setup *setup
	}

	type want struct {
		pkgDigest      string
		cacheFileCount int
		err            error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Should have crossplane.yaml and the expected number of files if successful.",
			args: args{
				cache: newLocalCache(
					WithFS(afero.NewMemMapFs()),
					WithRoot("/tmp/cache"),
					rootIsHome,
				),
				dep: dep1,
				pkg: pkg1,
			},
			want: want{
				pkgDigest:      pkg1.SHA,
				cacheFileCount: 2,
			},
		},
		"AddSecondDependency": {
			reason: "Should not return an error if we have multiple packages in cache.",
			args: args{
				cache: newLocalCache(
					WithFS(afero.NewMemMapFs()),
					WithRoot("/tmp/cache"),
					rootIsHome,
				),
				dep: dep2,
				pkg: pkg2,
				setup: &setup{
					dep: dep1,
					pkg: pkg1,
				},
			},
			want: want{
				pkgDigest:      pkg2.SHA,
				cacheFileCount: 4,
			},
		},
		"Replace": {
			reason: "Should not return an error if we're replacing the pre-existing image.",
			args: args{
				cache: newLocalCache(
					WithFS(afero.NewMemMapFs()),
					WithRoot("/tmp/cache"),
					rootIsHome,
				),
				dep: dep1,
				pkg: pkg2,
				setup: &setup{
					dep: dep1,
					pkg: pkg1,
				},
			},
			want: want{
				pkgDigest:      pkg2.SHA,
				cacheFileCount: 2,
			},
		},
		"ErrFailedCreate": {
			reason: "Should return an error if file creation fails.",
			args: args{
				cache: newLocalCache(
					WithFS(afero.NewReadOnlyFs(afero.NewMemMapFs())),
					WithRoot("/tmp/cache"),
					rootIsHome,
				),
				dep: dep1,
				pkg: pkg1,
			},
			want: want{
				err:            syscall.EPERM,
				cacheFileCount: 0,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.args.setup != nil {
				// establish a pre-existing entry
				err := tc.args.cache.Store(tc.args.setup.dep, tc.args.setup.pkg)
				if diff := cmp.Diff(nil, err, test.EquateErrors()); diff != "" {
					t.Errorf("\n%s\nStore(...): -want err, +got err:\n%s", tc.reason, diff)
				}
			}

			err := tc.args.cache.Store(tc.args.dep, tc.args.pkg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nStore(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			if tc.want.err == nil {

				e, _ := tc.args.cache.Get(tc.args.dep)

				if diff := cmp.Diff(tc.want.pkgDigest, e.Digest()); diff != "" {
					t.Errorf("\n%s\nStore(...): -want err, +got err:\n%s", tc.reason, diff)
				}

				if diff := cmp.Diff(tc.want.cacheFileCount, cacheFileCnt(tc.args.cache.fs, tc.args.cache.root)); diff != "" {
					t.Errorf("\n%s\nStore(...): -want err, +got err:\n%s", tc.reason, diff)
				}
			}
		})
	}
}

func TestClean(t *testing.T) {
	fs := afero.NewMemMapFs()
	cache, _ := NewLocal(
		WithFS(fs),
		WithRoot("~/.up/cache"),
	)
	readOnlyCache, _ := NewLocal(
		WithFS(afero.NewReadOnlyFs(fs)),
		WithRoot("~/.up/cache"),
	)

	type args struct {
		cache *Local
	}

	type want struct {
		preCleanFileCnt  int
		postCleanFileCnt int
		err              error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Success": {
			reason: "Should not return an error if cache was cleaned.",
			args: args{
				cache: cache,
			},
			want: want{
				preCleanFileCnt:  4,
				postCleanFileCnt: 0,
			},
		},
		"ErrFailedClean": {
			reason: "Should return an error if we failed to clean the cache.",
			args: args{
				cache: readOnlyCache,
			},
			want: want{
				preCleanFileCnt:  4,
				postCleanFileCnt: 4,
				err:              syscall.EPERM,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// add a few entries to cache
			e1 := cache.newEntry(pkg1)
			cache.add(e1, "index.docker.io/crossplane/provider-aws@v0.20.1-alpha")

			e2 := cache.newEntry(pkg2)
			cache.add(e2, "index.docker.io/crossplane/provider-gcp@v0.14.2")

			c := cacheFileCnt(fs, tc.args.cache.root)

			if diff := cmp.Diff(tc.want.preCleanFileCnt, c); diff != "" {
				t.Errorf("\n%s\nClean(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			err := tc.args.cache.Clean()

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nClean(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			c = cacheFileCnt(fs, tc.args.cache.root)

			if diff := cmp.Diff(tc.want.postCleanFileCnt, c); diff != "" {
				t.Errorf("\n%s\nClean(...): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestCalculatePath(t *testing.T) {
	tag1, _ := ociname.NewTag("crossplane/provider-aws:v0.20.1-alpha")
	tag2, _ := ociname.NewTag("gcr.io/crossplane/provider-gcp:v1.0.0")
	tag3, _ := ociname.NewTag("registry.upbound.io/examples-aws/getting-started:v0.14.0-240.g6a7366f")

	c, _ := NewLocal(
		WithFS(afero.NewMemMapFs()),
		WithRoot("/cache"),
		rootIsHome,
	)

	type args struct {
		tag *ociname.Tag
	}
	cases := map[string]struct {
		reason string
		args   args
		want   string
	}{
		"SuccessDockerIO": {
			reason: "Should return formatted cache path with packageName as filename.",
			args: args{
				tag: &tag1,
			},
			want: "index.docker.io/crossplane/provider-aws@v0.20.1-alpha",
		},
		"SuccessGCR": {
			reason: "Should return formatted cache path with packageName as filename.",
			args: args{
				tag: &tag2,
			},
			want: "gcr.io/crossplane/provider-gcp@v1.0.0",
		},
		"SuccessUpboundRegistry": {
			reason: "Should return formatted cache path with packageName as filename.",
			args: args{
				tag: &tag3,
			},
			want: "registry.upbound.io/examples-aws/getting-started@v0.14.0-240.g6a7366f",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := c.calculatePath(tc.args.tag)

			if diff := cmp.Diff(tc.want, d); diff != "" {
				t.Errorf("\n%s\nCalculatePath(...): -want err, +got err:\n%s", tc.reason, diff)
			}
		})
	}
}

func cacheFileCnt(fs afero.Fs, dir string) int {
	var cnt int
	afero.Walk(fs, dir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				cnt++
			}
			return nil
		})

	return cnt
}

var (
	rootIsHome = func(l *Local) {
		l.home = func() (string, error) {
			return "/", nil
		}
	}

	newLocalCache = func(opts ...Option) *Local {
		c, _ := NewLocal(opts...)
		return c
	}
)
