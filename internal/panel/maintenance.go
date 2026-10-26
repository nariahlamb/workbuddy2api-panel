// maintenance.go 模块级运维：
//
//  1. 进程重启（POST /panel/api/restart）——配合 magisk/restart.sh 整链重拉，
//     解决「改完配置必须开关机才生效」；
//  2. 凭证导入（GET/POST /panel/api/creds/import）——从手机公共目录批量吃外部
//     auth JSON（两种示例形态 + snake_case 历史形态），免 OAuth 直接进池；
//  3. 凭证导出（GET/POST /panel/api/creds/export）——把池内凭证写成与导入同形
//     的文件，落到公共目录，可搬去别的设备。
//
// 边界：这些都是「进程外」动作——重启靠 shell，导入导出只碰 auths/ 与公共目录，
// 不改变池的选号语义（导入走 Pool.Add，与 OAuth 添加账号同一条落池路径）。
package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// defaultImportDir 手机公共目录下的凭证交换目录（导入源 + 导出落点）。
// 放在 Download 下是为了让用户能用文件管理器直接拖文件进去，无需 root 即可见。
const defaultImportDir = "/storage/emulated/0/Download/wbauth"

// importDir 生效的导入/导出目录（配置覆盖，缺省用 defaultImportDir）。
func (p *Panel) importDir() string {
	if d := strings.TrimSpace(p.cfg.ImportDir); d != "" {
		return d
	}
	return defaultImportDir
}

// readCredentialFile 限制外部凭证文件体积，避免面板扫描任意大文件造成内存耗尽。
func readCredentialFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxCredentialBytes = 2 << 20
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxCredentialBytes {
		return nil, fmt.Errorf("文件过大（最大 %d MiB）", maxCredentialBytes>>20)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxCredentialBytes {
		return nil, fmt.Errorf("文件过大（最大 %d MiB）", maxCredentialBytes>>20)
	}
	return b, nil
}

// restartScript 生效的重启脚本路径（缺省模块目录下的 restart.sh）。
func (p *Panel) restartScript() string {
	if s := strings.TrimSpace(p.cfg.RestartScript); s != "" {
		return s
	}
	return "/data/adb/modules/workbuddy2api/restart.sh"
}

// ---------------------------------------------------------------------------
// 重启
// ---------------------------------------------------------------------------

// 重启后由前端轮询 overview，boot_id 改变即可判定新进程起来了。
//
// 为什么不同步等结果：restart.sh 会先杀掉本进程，同步等待必然断在半路。
// 所以这里只负责「把脚本拉起来且脱离父进程」，剩下的交给前端轮询。
//
// 安全约束：
//   - 只允许执行配置里给定的脚本（不接受请求体传路径，防任意命令执行）；
//   - 脚本必须存在且可执行，否则 501 让运维手工处理，绝不静默失败；
//   - 只在 panel 进程内 spawn，不做任何 shell 拼接。
func (p *Panel) restart(w http.ResponseWriter, r *http.Request) {
	script := p.restartScript()
	if fi, err := os.Stat(script); err != nil || fi.IsDir() || fi.Mode().Perm()&0o444 == 0 {
		writeErr(w, http.StatusNotImplemented, "重启脚本不可用或不可读: "+script+"（重新刷入模块可恢复）")
		return
	}
	cmd := exec.Command("/system/bin/sh", script)
	cmd.Dir = filepath.Dir(script)
	// 脱离：新会话 + 不继承本进程的 stdin/out/err，父进程被杀不影响它继续跑。
	cmd.SysProcAttr = detachSysProcAttr()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		writeErr(w, http.StatusInternalServerError, "拉起重启脚本失败: "+err.Error())
		return
	}
	// 不 Wait：本进程随即会被脚本杀掉，Wait 只会在信号中断时返回错误。
	go func() { _ = cmd.Wait() }()
	log.Printf("panel: 已发起进程重启（script=%s pid=%d）", script, cmd.Process.Pid)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"scheduled": true,
		"script":    script,
		"hint":      "进程将在 1~3 秒内退出并自动重拉，页面会自动重连",
	})
}

// ---------------------------------------------------------------------------
// 凭证导入
// ---------------------------------------------------------------------------

// credFile 导入目录里的一份候选文件（列表用）。
type credFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	UID      string `json:"uid,omitempty"`      // 解析出的账号 uid（失败为空）
	Nickname string `json:"nickname,omitempty"` // 解析出的昵称
	Realm    string `json:"realm,omitempty"`    // cn / global
	InPool   bool   `json:"in_pool"`            // 池内已有同 uid
	Error    string `json:"error,omitempty"`    // 解析失败原因（有值即不可导入）
}

// credsImportList 列出导入目录里可用的凭证文件（不落池，纯预览）。
// 目录不存在时返回空集 + dir，前端据此提示「把文件放进这个目录」。
func (p *Panel) credsImportList(w http.ResponseWriter, r *http.Request) {
	dir := p.importDir()
	files, err := p.scanImportDir(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取目录失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"dir":   dir,
		"files": files,
		"count": len(files),
	})
}

// scanImportDir 扫目录下的 *.json 并逐份试解析（只读，不入池）。
func (p *Panel) scanImportDir(dir string) ([]credFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []credFile{}, nil
		}
		return nil, err
	}
	out := make([]credFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		cf := credFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().Format(time.RFC3339)}
		raw, rerr := readCredentialFile(full)
		if rerr != nil {
			cf.Error = "读取失败: " + rerr.Error()
			out = append(out, cf)
			continue
		}
		a, perr := auth.ParseImported(raw)
		if perr != nil {
			cf.Error = perr.Error()
			out = append(out, cf)
			continue
		}
		cf.UID, cf.Nickname, cf.Realm = a.UID, a.Nickname, a.Realm()
		if _, ok := p.cfg.Pool.Status(a.UID); ok {
			cf.InPool = true
		}
		out = append(out, cf)
	}
	// 文件名排序：稳定顺序，前端表格不会每次都跳。
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// credsImport 执行导入。请求体：{"names":["a.json",...]}（空 = 导入目录下全部）。
//
// 每份文件独立成败：一个坏文件不影响其它文件（与批量刷新的容错口径一致）。
// 落盘口径与 OAuth 添加账号完全一致——auth.SaveAtomic 写 auths/，再 Pool.Add 进池，
// 免重启生效；同 uid 已存在时走 upsert（保留原池状态，只换凭证）。
func (p *Panel) credsImport(w http.ResponseWriter, r *http.Request) {
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	dir := p.importDir()
	var body struct {
		Names []string `json:"names"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
			return
		}
	}

	var targets []string
	if len(body.Names) == 0 {
		list, err := p.scanImportDir(dir)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取目录失败: "+err.Error())
			return
		}
		for _, f := range list {
			if f.Error == "" { // 解析失败的文件不参与「全部导入」
				targets = append(targets, f.Name)
			}
		}
	} else {
		targets = body.Names
	}
	if len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "没有可导入的文件（目录: "+dir+"）")
		return
	}

	type result struct {
		Name     string `json:"name"`
		OK       bool   `json:"ok"`
		UID      string `json:"uid,omitempty"`
		Nickname string `json:"nickname,omitempty"`
		Realm    string `json:"realm,omitempty"`
		Updated  bool   `json:"updated"` // true = 覆盖了已存在的账号
		Error    string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(targets))
	okCount := 0

	for _, name := range targets {
		res := result{Name: name}
		// 防路径穿越：只接受「文件名」，任何带分隔符的一律拒绝。
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." ||
			!strings.HasSuffix(strings.ToLower(name), ".json") {
			res.Error = "非法文件名（仅允许 .json 文件名）"
			results = append(results, res)
			continue
		}
		data, err := readCredentialFile(filepath.Join(dir, name))
		if err != nil {
			res.Error = "读取失败: " + err.Error()
			results = append(results, res)
			continue
		}
		a, err := auth.ParseImported(data)
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		_, existed := p.cfg.Pool.Status(a.UID)
		// 落盘文件名：与既有命名口径一致（workbuddy-<uid>.json）；
		// 已存在则复用池内原文件的 FilePath，避免同 uid 留下两份凭证文件。
		a.FilePath = filepath.Join(p.cfg.AuthDir, "workbuddy-"+a.UID+".json")
		if old := p.cfg.Pool.AuthByUID(a.UID); old != nil && old.FilePath != "" {
			a.FilePath = old.FilePath
		}
		if err := a.SaveAtomic(); err != nil {
			res.Error = "落盘失败: " + err.Error()
			results = append(results, res)
			continue
		}
		p.cfg.Pool.Add(a)
		p.cfg.Pool.Revive(a.UID) // 导入即人工恢复口径（与 OAuth 添加账号一致）
		res.OK, res.UID, res.Nickname, res.Realm = true, a.UID, a.Nickname, a.Realm()
		res.Updated = existed
		okCount++
		results = append(results, res)
	}

	log.Printf("panel: 凭证导入完成 %d/%d（来源 %s）", okCount, len(targets), dir)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       okCount > 0,
		"dir":      dir,
		"imported": okCount,
		"total":    len(targets),
		"results":  results,
	})
}

// ---------------------------------------------------------------------------
// 凭证导出
// ---------------------------------------------------------------------------

// credsExport 导出凭证到导入目录（同形，可直接再导入）。
//
// 请求体：{"uids":["..."],"all":false}；uids 为空或 all=true 时导出池内全部。
// 返回值里只给文件名与打码预览，**不回传完整 token**（避免 token 又落进浏览器
// 历史/localStorage）；完整内容只写在手机公共目录的文件里。
func (p *Panel) credsExport(w http.ResponseWriter, r *http.Request) {
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	dir := p.importDir()
	var body struct {
		UIDs []string `json:"uids"`
		All  bool     `json:"all"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
			return
		}
	}
	if len(body.UIDs) == 0 && !body.All {
		writeErr(w, http.StatusBadRequest, "未指定要导出的账号")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建导出目录失败: "+err.Error())
		return
	}
	// 目录保持普通公共目录权限（0755），不把现有 Download/wbauth 整目录 chmod 成可写；
	// 导出文件用 0644 是为了让 Android 文件管理器可见，UI 明确提示文件含完整 token。

	type result struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname,omitempty"`
		Realm    string `json:"realm,omitempty"`
		File     string `json:"file,omitempty"`
		Preview  string `json:"preview,omitempty"`
		Error    string `json:"error,omitempty"`
	}
	var results []result
	okCount := 0

	for _, s := range p.cfg.Pool.List() {
		if !body.All {
			found := false
			for _, u := range body.UIDs {
				if u == s.UID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		res := result{UID: s.UID, Nickname: s.Nickname, Realm: s.Realm}
		a := p.cfg.Pool.AuthByUID(s.UID)
		if a == nil {
			res.Error = "account not loaded"
			results = append(results, res)
			continue
		}
		doc := a.ExportShape()
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			res.Error = "序列化失败: " + err.Error()
			results = append(results, res)
			continue
		}
		// 文件名：workbuddy-<uid>.json（与导入源、auths/ 同口径，便于一眼对上）。
		name := "workbuddy-" + s.UID + ".json"
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, raw, 0o644); err != nil {
			res.Error = "写入失败: " + err.Error()
			results = append(results, res)
			continue
		}
		res.File = name
		res.Preview = auth.MaskToken(a.AccessTokenValue())
		okCount++
		results = append(results, res)
	}
	if results == nil {
		results = []result{}
	}
	log.Printf("panel: 凭证导出完成 %d 份（落点 %s）", okCount, dir)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       okCount > 0,
		"dir":      dir,
		"exported": okCount,
		"results":  results,
	})
}
