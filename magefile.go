//go:build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/magefile/mage/mg"
)

func MkdirDist() error {
	return os.MkdirAll("dist", 0o770)
}

func All() {
	mg.Deps(Containers, Debs)
	mg.Deps(Checksums)
}

func Binaries() error {
	mg.Deps(MkdirDist)

	verStr, _, _, _, _, err := version()
	if err != nil {
		return err
	}

	cmd := exec.Command("docker", "compose", "-f", "docker-compose-build.yml", "up")
	cmd.Env = append(os.Environ(),
		"VERSION="+verStr,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Containers() error {
	verStr, maj, min, patch, prerel, err := version()
	if err != nil {
		return err
	}

	mg.Deps(MkdirDist, Binaries)

	args := []string{
		"buildx", "build",
		"--build-arg", "VERSION=" + verStr,
	}
	if prerel == "" {
		args = append(args,
			"--tag", "gitea.drjosh.dev/josh/jrouter:latest",
			"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%d.%d.%d", maj, min, patch),
			"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%d.%d", maj, min),
			"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%d", maj),
		)
	} else {
		args = append(args,
			"--tag", fmt.Sprintf("gitea.drjosh.dev/josh/jrouter:%s", prerel),
		)
	}
	args = append(args,
		"--platform", "linux/arm64/v8,linux/amd64",
		"--builder", "container",
		"--push", ".",
	)

	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Debs() error {
	mg.Deps(
		func() error { return Deb("amd64") },
		func() error { return Deb("arm64") },
	)
	return nil
}

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

func Clean() {
	os.RemoveAll("tmp")
	os.RemoveAll("dist")
}

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
