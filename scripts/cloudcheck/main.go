// cloudcheck membuktikan bahwa kode cloud yang ter-comment benar-benar siap dipakai:
// ia menyalin proyek ke folder sementara, MEMBUKA semua baris berawalan `// CLOUD: `,
// lalu menjalankan `go build` dan `go vet` pada salinan itu. Proyek aslinya tidak diubah.
//
//	go run ./scripts/cloudcheck          # bangun versi cloud di folder sementara
//	go run ./scripts/cloudcheck -list    # daftar semua baris CLOUD per file
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var marker = regexp.MustCompile(`^(\s*)// CLOUD: ?`)

func main() {
	list := flag.Bool("list", false, "tampilkan semua baris CLOUD, tanpa membangun")
	flag.Parse()

	if *list {
		if err := listLines("."); err != nil {
			fatal(err)
		}
		return
	}

	tmp, err := os.MkdirTemp("", "ledger-cloudcheck-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(tmp)

	enabled := 0
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "data", "node_modules", "bin":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(tmp, path), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".go") {
			var out []string
			for _, line := range strings.Split(string(raw), "\n") {
				if marker.MatchString(line) {
					enabled++
				}
				out = append(out, marker.ReplaceAllString(line, "$1"))
			}
			raw = []byte(strings.Join(out, "\n"))
		}
		return os.WriteFile(filepath.Join(tmp, path), raw, 0o644)
	})
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%d baris CLOUD dibuka pada salinan sementara: %s\n", enabled, tmp)

	for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = tmp, os.Stdout, os.Stderr
		fmt.Println("> go", strings.Join(args, " "))
		if err := cmd.Run(); err != nil {
			fmt.Println("GAGAL: kode cloud belum bisa dibangun. Perbaiki sebelum migrasi.")
			os.Exit(1)
		}
	}
	fmt.Println("OK: versi cloud berhasil di-build dan lolos vet.")
}

func listLines(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "data" || n == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		n, count := 0, 0
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			n++
			if marker.MatchString(sc.Text()) {
				count++
			}
		}
		if count > 0 {
			fmt.Printf("%4d baris  %s\n", count, filepath.ToSlash(path))
		}
		return sc.Err()
	})
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
