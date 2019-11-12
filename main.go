package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

func findGo() string {
	path, _ := exec.LookPath("go")
	return path
}

var (
	gobin     = flag.String("go", findGo(), "Go binary")
	nobuild   = flag.Bool("nobuild", false, "skip go build")
	noarchive = flag.Bool("noarchive", false, "skip archiving")
)

var targets = []struct{ os, arch string }{
	{"darwin", "amd64"},
	{"freebsd", "amd64"},
	{"linux", "386"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"openbsd", "amd64"},
	{"windows", "386"},
	{"windows", "amd64"},
}

const relver = "v1.5.0-rc1"

const ldflags = `-buildid= ` +
	`-X github.com/decred/dcrd/internal/version.BuildMetadata=release ` +
	`-X github.com/decred/dcrd/internal/version.PreRelease=rc1 ` +
	`-X github.com/decred/dcrwallet/version.BuildMetadata=release ` +
	`-X github.com/decred/dcrwallet/version.PreRelease=rc1 ` +
	`-X github.com/decred/dcrlnd/build.BuildMetadata=release ` +
	`-X github.com/decred/dcrlnd/build.PreRelease=rc1`

const tags = "safe,netgo"

var tools = []struct{ tool, builddir string }{
	{"decred.org/dcrwallet", "./dcrwallet"},
	{"github.com/decred/dcrd", "./dcrd"},
	{"github.com/decred/dcrd/cmd/dcrctl", "./dcrd"},
	{"github.com/decred/dcrd/cmd/promptsecret", "./dcrd"},
	{"github.com/decred/dcrlnd/cmd/dcrlnd", "./dcrlnd"},
}

type manifestLine struct {
	name string
	hash [32]byte
}

type manifest []manifestLine

func main() {
	flag.Parse()
	logvers()
	var m manifest
	for i := range targets {
		for j := range tools {
			if *nobuild {
				break
			}
			build(tools[j].tool, targets[i].os, targets[i].arch, tools[j].builddir)
		}
		if *noarchive {
			continue
		}
		archive(targets[i].os, targets[i].arch, &m)
	}
	if len(m) > 0 {
		writeManifest(m)
	}
}

func logvers() {
	output, err := exec.Command(*gobin, "version").CombinedOutput()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("releasing with %s %s", *gobin, output)
}

func exeName(module, goos string) string {
	exe := path.Base(module) // TODO: fix for /v2+
	if goos == "windows" {
		exe += ".exe"
	}
	return exe
}

func build(tool, goos, arch, builddir string) {
	exe := exeName(tool, goos)
	out := filepath.Join("..", "bin", goos+"-"+arch, exe)
	log.Printf("build: %s", out[3:]) // trim off leading "../"
	gocmd(goos, arch, builddir, "build", "-trimpath", "-tags", tags, "-o", out, "-ldflags", ldflags, tool)
}

func gocmd(goos, arch, builddir string, args ...string) {
	os.Setenv("GOOS", goos)
	os.Setenv("GOARCH", arch)
	os.Setenv("CGO_ENABLED", "0")
	os.Setenv("GOFLAGS", "")
	cmd := exec.Command(*gobin, args...)
	cmd.Dir = builddir
	output, err := cmd.CombinedOutput()
	if len(output) != 0 {
		log.Printf("go '%s'\n%s", strings.Join(args, `' '`), output)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func archive(goos, arch string, m *manifest) {
	if _, err := os.Stat("archive"); os.IsNotExist(err) {
		err := os.Mkdir("archive", 0777)
		if err != nil {
			log.Fatal(err)
		}
	}
	if goos == "windows" {
		archiveZip(goos, arch, m)
		return
	}
	tarPath := fmt.Sprintf("decred-%s-%s-%s", goos, arch, relver)
	tarFile, err := os.Create(fmt.Sprintf("archive/%s.tar", tarPath))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("archive: %v", tarFile.Name()+".gz")
	tw := tar.NewWriter(tarFile)
	hdr := &tar.Header{
		Name:     tarPath + "/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
		Format:   tar.FormatPAX,
	}
	err = tw.WriteHeader(hdr)
	if err != nil {
		log.Fatal(err)
	}
	for i := range tools {
		exe := exeName(tools[i].tool, goos)
		exePath := filepath.Join("bin", goos+"-"+arch, exe)
		info, err := os.Stat(exePath)
		if err != nil {
			log.Fatal(err)
		}
		exeFi, err := os.Open(exePath)
		if err != nil {
			log.Fatal(err)
		}
		hdr := &tar.Header{
			Name:     strings.ReplaceAll(filepath.Join(tarPath, exe), `\`, `/`),
			Typeflag: tar.TypeReg,
			Mode:     0755,
			Size:     info.Size(),
			Format:   tar.FormatPAX,
		}
		err = tw.WriteHeader(hdr)
		if err != nil {
			log.Fatal(err)
		}
		_, err = io.Copy(tw, exeFi)
		if err != nil {
			log.Fatal(err)
		}
		exeFi.Close()
	}
	err = tw.Close()
	if err != nil {
		log.Fatal(err)
	}
	zf, err := os.Create(tarFile.Name() + ".gz")
	if err != nil {
		log.Fatal(err)
	}
	hash := sha256.New()
	defer func() {
		name := filepath.Base(tarFile.Name()) + ".gz"
		var sum [32]byte
		copy(sum[:], hash.Sum(nil))
		*m = append(*m, manifestLine{name, sum})
	}()
	w := io.MultiWriter(zf, hash)
	zw := gzip.NewWriter(w)
	_, err = tarFile.Seek(0, os.SEEK_SET)
	if err != nil {
		log.Fatal(err)
	}
	_, err = io.Copy(zw, tarFile)
	if err != nil {
		log.Fatal(err)
	}
	err = zw.Close()
	if err != nil {
		log.Fatal(err)
	}
	err = tarFile.Close()
	if err != nil {
		log.Fatal(err)
	}
	err = os.Remove(tarFile.Name())
	if err != nil {
		log.Fatal(err)
	}
}

func archiveZip(goos, arch string, m *manifest) {
	zipPath := fmt.Sprintf("decred-%s-%s-%s", goos, arch, relver)
	zipFile, err := os.Create(fmt.Sprintf("archive/%s.zip", zipPath))
	defer zipFile.Close()
	if err != nil {
		log.Fatal(err)
	}
	hash := sha256.New()
	w := io.MultiWriter(zipFile, hash)
	defer func() {
		name := filepath.Base(zipFile.Name())
		var sum [32]byte
		copy(sum[:], hash.Sum(nil))
		*m = append(*m, manifestLine{name, sum})
	}()
	log.Printf("archive: %v", zipFile.Name())
	zw := zip.NewWriter(w)
	for i := range tools {
		exe := exeName(tools[i].tool, goos)
		exePath := filepath.Join("bin", goos+"-"+arch, exe)
		exeFi, err := os.Open(exePath)
		if err != nil {
			log.Fatal(err)
		}
		h := &zip.FileHeader{
			Name:   strings.ReplaceAll(filepath.Join(zipPath, exe), `\`, `/`),
			Method: zip.Deflate,
		}
		f, err := zw.CreateHeader(h)
		if err != nil {
			log.Fatal(err)
		}
		_, err = io.Copy(f, exeFi)
		if err != nil {
			log.Fatal(err)
		}
		exeFi.Close()
	}
	err = zw.Close()
	if err != nil {
		log.Fatal(err)
	}
}

func writeManifest(m manifest) {
	fi, err := os.Create(fmt.Sprintf("archive/manifest-%s.txt", relver))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("manifest: %v", fi.Name())
	for i := range m {
		_, err = fmt.Fprintf(fi, "%x  %s\n", m[i].hash, m[i].name)
		if err != nil {
			log.Fatal(err)
		}
	}
	err = fi.Close()
	if err != nil {
		log.Fatal(err)
	}
}
