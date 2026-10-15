package iso

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/theme"
)

var update = flag.Bool("update", false, "rewrite golden files")

// lbConfig runs auto/config with a stand-in for live-build's lb that prints
// the options it gets, one per line.
func lbConfig(t *testing.T, env ...string) (string, error) {
	t.Helper()
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "lb"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "auto/config", "--debug")
	cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin"}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Every image's lb config, so a change to what the images are made of shows
// up in review.
func TestLiveBuildConfig(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, variant := range []string{"standard", "firmware"} {
			name := arch + "-" + variant
			out, err := lbConfig(t, "RACKSAIL_ARCH="+arch, "RACKSAIL_VARIANT="+variant)
			if err != nil {
				t.Fatalf("%s: %v\n%s", name, err, out)
			}
			golden := filepath.Join("testdata", "lb-config-"+name+".golden")
			if *update {
				os.MkdirAll("testdata", 0o755)
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("%s: lb config differs from %s; run go test ./iso -update after an intended change\n%s", name, golden, out)
			}
			for _, must := range []string{"noauto", "--uefi-secure-boot\nenable", "--distribution\ntrixie", "--debian-installer\nnone"} {
				if !strings.Contains(out, must+"\n") {
					t.Errorf("%s: lb config lacks %q", name, must)
				}
			}
			if strings.Contains(out, "non-free-firmware") != (variant == "firmware") {
				t.Errorf("%s: only the firmware image takes non-free-firmware", name)
			}
			if strings.Contains(out, "grub-pc") != (arch == "amd64") {
				t.Errorf("%s: only amd64 boots with BIOS", name)
			}
		}
	}
	for _, env := range [][]string{{"RACKSAIL_ARCH=riscv64", "RACKSAIL_VARIANT=standard"}, {"RACKSAIL_ARCH=amd64", "RACKSAIL_VARIANT=full"}, nil} {
		if out, err := lbConfig(t, env...); err == nil {
			t.Errorf("%v should fail:\n%s", env, out)
		}
	}
	out, _ := lbConfig(t, "RACKSAIL_ARCH=amd64", "RACKSAIL_VARIANT=standard", "RACKSAIL_MIRROR=http://mirror.example.test/debian/")
	if strings.Count(out, "http://mirror.example.test/debian/\n") != 3 {
		t.Errorf("the mirror applies to bootstrapping, the live system and its sources:\n%s", out)
	}
}

func lines(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

func TestPackageLists(t *testing.T) {
	lists, _ := filepath.Glob("config/package-lists/*.list.chroot")
	if len(lists) < 2 {
		t.Fatalf("package lists = %v", lists)
	}
	seen := map[string]string{}
	for _, list := range lists {
		for _, line := range lines(t, list) {
			line = strings.TrimSpace(line)
			if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#if") && !strings.HasPrefix(line, "#nif") && !strings.HasPrefix(line, "#endif")) {
				continue
			}
			if strings.HasPrefix(line, "#") {
				t.Errorf("%s: %q: the lists don't use conditionals", list, line)
				continue
			}
			if !packageName.MatchString(line) {
				t.Errorf("%s: %q isn't a package name", list, line)
			}
			if other, ok := seen[line]; ok {
				t.Errorf("%s is listed in %s and %s", line, other, list)
			}
			seen[line] = list
		}
	}
	if _, ok := seen["live-config"]; ok {
		t.Error("live-config would create a user with a known password")
	}
	for _, must := range []string{"live-boot", "mmdebstrap", "btrfs-progs", "plymouth", "restic", "systemd-resolved"} {
		if _, ok := seen[must]; !ok {
			t.Errorf("the installer image needs %s", must)
		}
	}
}

// live-build fills in @...@ values in every .cfg file of the boot menu, and
// also replaces older forms of the same names written without @ and every
// "//". Anything like that in our files would be rewritten.
func TestBootMenu(t *testing.T) {
	cfgs, _ := filepath.Glob("config/bootloaders/grub-pc/*.cfg")
	if len(cfgs) != 3 {
		t.Fatalf("boot menu files = %v", cfgs)
	}
	placeholder := regexp.MustCompile(`@[A-Z_]+@`)
	known := []string{"@KERNEL_LIVE@", "@INITRD_LIVE@", "@APPEND_LIVE@", "@APPEND_VERIFY_CHECKSUMS@"}
	clobbered := []string{"KERNEL_LIVE", "INITRD_LIVE", "APPEND_LIVE", "LINUX_LIVE", "LINUX_INSTALL", "MEMTEST", "KERNEL_GI", "KERNEL_DI", "LB_BOOTAPPEND"}
	ids := map[string]bool{}
	for _, cfg := range cfgs {
		data, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, p := range placeholder.FindAllString(text, -1) {
			if !slices.Contains(known, p) {
				t.Errorf("%s: live-build doesn't fill in %s", cfg, p)
			}
		}
		rest := placeholder.ReplaceAllString(text, "")
		for _, word := range clobbered {
			if strings.Contains(rest, word) {
				t.Errorf("%s: live-build would rewrite %s", cfg, word)
			}
		}
		if strings.Contains(text, "//") {
			t.Errorf("%s: live-build would turn // into /", cfg)
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.HasSuffix(line, " ") {
				t.Errorf("%s: live-build strips trailing spaces: %q", cfg, line)
			}
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == "linux" && !strings.Contains(line, "console=${racksail_serial},115200n8") {
				t.Errorf("%s: every entry runs the installer on the serial console too: %q", cfg, line)
			}
			if m := regexp.MustCompile(`--id=([a-z]+)`).FindStringSubmatch(line); m != nil {
				ids[m[1]] = true
			}
		}
	}
	config, _ := os.ReadFile("config/bootloaders/grub-pc/config.cfg")
	if m := regexp.MustCompile(`set default=(\S+)`).FindSubmatch(config); m == nil || !ids[string(m[1])] {
		t.Errorf("the default entry must exist: %v", ids)
	}
	// Every font the theme names is loaded and made by build.sh.
	themeText, _ := os.ReadFile("branding/grub/theme.txt")
	build, _ := os.ReadFile("build.sh")
	for _, m := range regexp.MustCompile(`"Inter (Regular|Bold) ([0-9]+)"`).FindAllStringSubmatch(string(themeText), -1) {
		file := fmt.Sprintf("inter-%s-%s.pf2", strings.ToLower(m[1]), m[2])
		if !strings.Contains(string(config), "/boot/grub/racksail/"+file) {
			t.Errorf("config.cfg doesn't load %s", file)
		}
		if !strings.Contains(string(build), file) {
			t.Errorf("build.sh doesn't make %s", file)
		}
	}
	for _, image := range []string{"background.png", "select_c.png", "select_nw.png", "select_se.png"} {
		if !strings.Contains(string(build), image) {
			t.Errorf("build.sh doesn't render %s", image)
		}
	}
}

// The installer's console colors are the default theme's console palette.
func TestConsolePalette(t *testing.T) {
	rows := lines(t, "branding/console/console.vtrgb")
	if len(rows) != 3 {
		t.Fatalf("console.vtrgb has %d rows, want red, green and blue", len(rows))
	}
	var colors [16][3]int
	for c, row := range rows {
		values := strings.Split(row, ",")
		if len(values) != 16 {
			t.Fatalf("row %d has %d values, want 16", c, len(values))
		}
		for i, v := range values {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 255 {
				t.Fatalf("row %d, color %d: %q", c, i, v)
			}
			colors[i][c] = n
		}
	}
	hex := func(i int) string { return fmt.Sprintf("#%02X%02X%02X", colors[i][0], colors[i][1], colors[i][2]) }
	console := theme.Default().Console
	for i, want := range map[int]theme.Color{0: console.Background, 1: console.Danger, 2: console.Success, 3: console.Warning, 4: console.Accent, 7: console.Foreground} {
		if !strings.EqualFold(hex(i), string(want)) {
			t.Errorf("console color %d is %s, the theme's is %s", i, hex(i), want)
		}
	}
}

func TestLiveSystemFiles(t *testing.T) {
	root := "config/includes.chroot_after_packages"
	for _, unit := range []string{"etc/systemd/system/getty@tty1.service.d/racksail-installer.conf", "etc/systemd/system/serial-getty@.service.d/racksail-installer.conf"} {
		data, err := os.ReadFile(filepath.Join(root, unit))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "ExecStart=\nExecStart=-/usr/local/bin/racksail install\n") || !strings.Contains(text, "RACKSAIL_CONSOLE=%I") {
			t.Errorf("%s must replace the login prompt with racksail install:\n%s", unit, text)
		}
	}
	hook, _ := os.ReadFile("config/hooks/normal/9000-racksail.hook.chroot")
	if !strings.Contains(string(hook), "plymouth-set-default-theme racksail") || !strings.Contains(string(hook), "racksail-console.service") {
		t.Error("the hook must set the splash and enable the console colors")
	}
	plymouth, _ := os.ReadFile("branding/plymouth/racksail.plymouth")
	if !strings.Contains(string(plymouth), "ScriptFile=/usr/share/plymouth/themes/racksail/racksail.script") {
		t.Error("the splash must name its script where build.sh installs it")
	}
	script, _ := os.ReadFile("branding/plymouth/racksail.script")
	build, _ := os.ReadFile("build.sh")
	for _, m := range regexp.MustCompile(`Image\("([a-z-]+\.png)"\)`).FindAllStringSubmatch(string(script), -1) {
		if !strings.Contains(string(build), m[1]) {
			t.Errorf("the splash uses %s, which build.sh doesn't render", m[1])
		}
	}
}

func TestScriptsParse(t *testing.T) {
	scripts := map[string]string{
		"auto/config": "sh",
		"config/hooks/normal/9000-racksail.hook.chroot":      "sh",
		"config/hooks/normal/9900-racksail-trim.hook.chroot": "sh",
		"build.sh":        "bash",
		"in-container.sh": "bash",
	}
	for file, shell := range scripts {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s must be executable", file)
		}
		if out, err := exec.Command(shell, "-n", file).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", file, err, out)
		}
	}
}
