package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/config"
	"github.com/AoManoh/openpe/internal/update"
	"github.com/AoManoh/openpe/internal/version"
)

// runUpdate 实现 `openpe update`（业务契约 U1）：默认直接执行升级——查询
// module proxy 的最新发布版本，代跑 `go install` 安装 openpe 与
// openpe-server 两个二进制；--check 仅查询提示；--refresh-cache 是 hook
// 交付后 detached 刷新缓存用的内部静默模式。所有失败明确外抛，永不静默，
// 永不降级安装。
func runUpdate(args []string, stdout io.Writer, stderr io.Writer, runCmd commandRunner) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	checkOnly := fs.Bool("check", false, "only check for a newer release, do not install")
	refreshCache := fs.Bool("refresh-cache", false, "internal: refresh the notice cache silently and exit")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	current := version.Value()
	info, fetchErr := update.Client{}.Latest(context.Background(), update.ModulePath)

	// 内部静默模式：只维护缓存，不打印任何内容（宿主是 hook 交付后的
	// detached 子进程，输出无人消费）。失败以退出码区分，提醒缺席即静默。
	if *refreshCache {
		if fetchErr != nil {
			return 1
		}
		saveCheckState(cfg, info.Version)
		return 0
	}

	if fetchErr != nil {
		fmt.Fprintln(stderr, updateFetchFailureMessage(fetchErr, cfg.Language))
		return 1
	}
	saveCheckState(cfg, info.Version)
	latest := info.Version
	newer := update.IsNewer(latest, current)
	comparable := current != version.Devel

	if *checkOnly {
		fmt.Fprintln(stdout, updateCheckMessage(current, latest, newer, comparable, cfg.Language))
		return 0
	}
	if comparable && !newer {
		// 已是最新或远端更旧：报告事实，永不降级安装（业务契约 U1.6）。
		fmt.Fprintln(stdout, updateNoActionMessage(current, latest, cfg.Language))
		return 0
	}
	// current 为 devel（本地构建/无法判定）时允许安装：用户显式要求升级，
	// 安装 @<latest> 是确定性动作，不构成降级判定问题（DD-V3）。
	if err := renameRunningSelfOnWindows(); err != nil {
		fmt.Fprintln(stderr, updateSelfRenameFailureMessage(err, latest, cfg.Language))
		return 1
	}
	for _, target := range []string{"cmd/openpe", "cmd/openpe-server"} {
		pkg := update.ModulePath + "/" + target + "@" + latest
		if err := runCmd(context.Background(), "go", []string{"install", pkg}, strings.NewReader(""), stdout, stderr); err != nil {
			fmt.Fprintln(stderr, updateInstallFailureMessage(pkg, err, cfg.Language))
			return 1
		}
	}
	fmt.Fprintln(stdout, updateSuccessMessage(latest, cfg.Language))
	return 0
}

// saveCheckState 尽力写入提醒缓存；缓存写失败不影响 update 主流程（提醒是
// 辅助能力），但绝不影响升级本身的成败判定。
func saveCheckState(cfg config.Config, latest string) {
	path, err := update.StatePath(cfg.Delivery.CacheDir)
	if err != nil {
		return
	}
	_ = update.SaveState(path, update.CheckState{CheckedAt: time.Now(), Latest: latest})
}

// renameRunningSelfOnWindows 处理 Windows 不能覆盖运行中 exe 的限制：升级前
// 把自身改名为 .old（Windows 允许改名运行中的 exe），go install 即可写入新
// 文件；顺带清理上次遗留的 .old。非 Windows 平台是 no-op。
func renameRunningSelfOnWindows() error {
	exe, err := os.Executable()
	if err == nil {
		_ = os.Remove(exe + ".old")
	}
	if runtime.GOOS != "windows" {
		return nil
	}
	if err != nil {
		return err
	}
	return os.Rename(exe, exe+".old")
}

func updateFetchFailureMessage(err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openpe update failed to query the module proxy: %v\nManual upgrade: go install %s/cmd/openpe@latest && go install %s/cmd/openpe-server@latest", err, update.ModulePath, update.ModulePath)
	}
	return fmt.Sprintf("openpe update 查询 module proxy 失败：%v\n手动升级：go install %s/cmd/openpe@latest && go install %s/cmd/openpe-server@latest", err, update.ModulePath, update.ModulePath)
}

func updateCheckMessage(current string, latest string, newer bool, comparable bool, language string) string {
	en := isEnglishLanguage(language)
	switch {
	case newer:
		if en {
			return fmt.Sprintf("New release available: %s (current %s). Run `openpe update` to upgrade.", latest, current)
		}
		return fmt.Sprintf("发现新版本 %s（当前 %s）。运行 openpe update 升级。", latest, current)
	case !comparable:
		if en {
			return fmt.Sprintf("Latest release: %s. Current version is %s (local build, not comparable); run `openpe update` to install the release build.", latest, current)
		}
		return fmt.Sprintf("最新发布版本 %s。当前为本地构建（%s，无法比较）；运行 openpe update 可安装发布版本。", latest, current)
	default:
		if en {
			return fmt.Sprintf("Already up to date (current %s, latest %s).", current, latest)
		}
		return fmt.Sprintf("已是最新（当前 %s，最新 %s）。", current, latest)
	}
}

func updateNoActionMessage(current string, latest string, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("Already up to date (current %s, latest %s); nothing to install. openpe never downgrades.", current, latest)
	}
	return fmt.Sprintf("已是最新（当前 %s，最新 %s），无需安装。openpe 不会执行降级。", current, latest)
}

func updateSelfRenameFailureMessage(err error, latest string, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openpe update could not move the running executable aside (%v).\nManual upgrade: go install %s/cmd/openpe@%s && go install %s/cmd/openpe-server@%s", err, update.ModulePath, latest, update.ModulePath, latest)
	}
	return fmt.Sprintf("openpe update 无法移开运行中的可执行文件（%v）。\n手动升级：go install %s/cmd/openpe@%s && go install %s/cmd/openpe-server@%s", err, update.ModulePath, latest, update.ModulePath, latest)
}

func updateInstallFailureMessage(pkg string, err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openpe update failed while running `go install %s`: %v\nFix the error above or run the command manually.", pkg, err)
	}
	return fmt.Sprintf("openpe update 执行 `go install %s` 失败：%v\n请根据上方错误处理，或手动执行该命令。", pkg, err)
}

func updateSuccessMessage(latest string, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("Upgraded openpe and openpe-server to %s (installed into your Go bin directory). Verify with `openpe --version`; restart openpe-server if it is running.", latest)
	}
	return fmt.Sprintf("已将 openpe 与 openpe-server 升级到 %s（安装到 Go bin 目录）。用 openpe --version 验证；openpe-server 若在运行请重启。", latest)
}
