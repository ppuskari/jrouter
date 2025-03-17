//go:build mage

/*
   Copyright 2025 Josh Deprez

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

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/magefile/mage/mg"
)

// MkdirDist ensures ./dist exists.
func MkdirDist() error {
	return os.MkdirAll("dist", 0o770)
}

// All builds binaries, container images, .deb archives, and checksums.txt.
func All() {
	mg.Deps(Containers, Debs)
	mg.Deps(Checksums)
}

// AllWithPush is like All, but pushes container images to the registry.
// It does not (yet) push binaries, .debs, or checksums.txt to a release.
func All() {
	mg.Deps(ContainersWithPush, Debs)
	mg.Deps(Checksums)
}

// Binary directly builds a binary for the current platform.
func Binary() error {
	mg.Deps(MkdirDist)

	verStr, _, _, _, prerel, err := version()
	if err != nil {
		return err
	}

	binName := fmt.Sprintf("jrouter_%s_%s_%s", verStr, runtime.GOOS, runtime.GOARCH)

	args := []string{
		"build", "-v", "-o", filepath.Join("dist", binName),
	}

	if prerel == "" {
		// Strip symbols/DWARF, enable PIE, static linking.
		args = append(args, "-ldflags", "-s -w -extldflags=-static")
	} else {
		// Only static linking.
		args = append(args, "-ldflags", "-extldflags=-static")
	}

	args = append(args, ".")

	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// LinuxBinary builds a Linux binary for the current arch using a Docker
// container.
func LinuxBinary() error {
	mg.Deps(MkdirDist)

	cmd := exec.Command("docker", "compose", "-f", "docker-compose-build.yml", "up", "build-"+runtime.GOARCH)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Binaries builds binaries for all supported platforms (linux_{arm64,amd64}).
func Binaries() error {
	mg.Deps(MkdirDist)

	cmd := exec.Command("docker", "compose", "-f", "docker-compose-build.yml", "up")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Container builds a container image for the current arch and loads it into the
// local Docker.
func Container() error {
	mg.Deps(MkdirDist, LinuxBinary)

	buildArgs, err := dockerBuildArgs()
	if err != nil {
		return err
	}

	args := append(buildArgs, ".")

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Containers builds container images for all supported platforms
// (linux_{arm64,amd64})..
func Containers() error {
	mg.Deps(MkdirDist, Binaries)

	args, err := dockerBuildxArgs(false)
	if err != nil {
		return err
	}

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ContainersWithPush builds container images and pushes to the registry.
func ContainersWithPush() error {
	mg.Deps(MkdirDist, Binaries)

	// I wish mage had optional args.
	args, err := dockerBuildxArgs(true)
	if err != nil {
		return err
	}

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dockerBuildArgs() ([]string, error) {
	verStr, maj, min, patch, prerel, err := version()
	if err != nil {
		return nil, err
	}
	args := []string{
		"build",
		"--build-arg", "VERSION=" + verStr,
		"--file", "Dockerfile-prebuilt-binary",
	}

	if prerel != "" {
		return append(args,
			"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%s", prerel),
		), nil
	}
	return append(args,
		"--tag", "gitea.drjosh.dev/josh/jrouter:latest",
		"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%d.%d.%d", maj, min, patch),
		"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%d.%d", maj, min),
		"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%d", maj),
	), nil
}

func dockerBuildxArgs(push bool) ([]string, error) {
	buildArgs, err := dockerBuildArgs()
	if err != nil {
		return nil, err
	}

	args := []string{"buildx"}
	args = append(args, buildArgs...)
	args = append(args,
		"--platform", "linux/arm64/v8,linux/amd64",
		"--builder", "container",
	)
	if push {
		args = append(args, "--push")
	}
	args = append(args, ".")

	return args, nil
}

// Debs builds .deb archives for all supported platforms (arm64, amd64).
func Debs() error {
	mg.Deps(
		func() error { return Deb("amd64") },
		func() error { return Deb("arm64") },
	)
	return nil
}

// Deb builds a .deb archive for a specific arch (required argument)
func Deb(arch string) error {
	verStr, _, _, _, _, err := version()
	if err != nil {
		return err
	}

	mg.Deps(MkdirDist, Binaries)

	cmd := exec.Command("go", "tool", "nfpm", "package",
		"--config", "packaging/nfpm.yaml",
		"--packager", "deb",
		"--target", "dist",
	)
	cmd.Env = append(os.Environ(),
		"VERSION="+verStr,
		"ARCH="+arch,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Checksums produces a checksums.txt file for all other files in ./dist.
func Checksums() error {
	mg.Deps(MkdirDist)

	cf, err := os.Create("dist/checksums.txt")
	if err != nil {
		return err
	}
	defer cf.Close()

	distEnts, err := os.ReadDir("dist")
	if err != nil {
		return err
	}
	var files []string
	for _, ent := range distEnts {
		if ent.Name() == "checksums.txt" {
			continue
		}
		files = append(files, ent.Name())
	}

	cmd := exec.Command("sha256sum", files...)
	cmd.Dir = "dist"
	cmd.Stdout = cf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return cf.Close()
}

// Clean removes ./tmp and ./dist.
func Clean() {
	os.RemoveAll("tmp")
	os.RemoveAll("dist")
}

// version reads and parses meta/VERSION.
func version() (verStr string, maj, min, patch int, prerel string, err error) {
	verBytes, err := os.ReadFile("meta/VERSION")
	if err != nil {
		return "", 0, 0, 0, "dev", fmt.Errorf("read meta/VERSION: %w", err)
	}
	verStr = strings.TrimSpace(string(verBytes))
	if verStr == "" {
		return "", 0, 0, 0, "dev", errors.New("meta/VERSION file empty")
	}
	core, prerel, _ := strings.Cut(verStr, "-")
	coreBits := strings.Split(core, ".")
	if len(coreBits) != 3 {
		return "", 0, 0, 0, "dev", fmt.Errorf("malformed version string %q", verStr)
	}
	maj, err0 := strconv.Atoi(coreBits[0])
	min, err1 := strconv.Atoi(coreBits[1])
	patch, err2 := strconv.Atoi(coreBits[2])
	return verStr, maj, min, patch, prerel, errors.Join(err0, err1, err2)
}
