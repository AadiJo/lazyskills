package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lazyskills/internal/compat"
	"lazyskills/internal/model"
)

type CommandPreview struct {
	ID              string
	Title           string
	Program         string
	Args            []string
	Exec            ExecSpec
	Command         string
	Description     string
	Mutates         bool
	RequiresConfirm bool
	Dangerous       bool
	ConfirmValue    string
	Available       bool
	Reason          string
}

type ExecSpec struct {
	Program     string
	Args        []string
	Cwd         string
	Interactive bool
	Internal    string
}

type SkillsResolver func() (program string, baseArgs []string)

func ForSkill(sk *model.Skill) []CommandPreview {
	return ForSkillWithResolver(sk, ResolveSkillsCommand)
}

func ForSkillWithResolver(sk *model.Skill, resolve SkillsResolver) []CommandPreview {
	if sk == nil {
		return nil
	}
	if resolve == nil {
		resolve = ResolveSkillsCommand
	}
	previews := []CommandPreview{}
	if open, ok, reason := openEditorAction(sk); ok {
		previews = append(previews, open)
	} else {
		previews = append(previews, unavailablePreview("Open selected skill", reason))
	}

	if addSource, skillFilter, ok, reason := addIdentity(sk); ok {
		program, baseArgs := resolve()
		args := append([]string{}, baseArgs...)
		args = append(args, "add", addSource, "--skill", skillFilter, "--yes")
		if sk.Scope == model.ScopeGlobal {
			args = append(args, "-g")
		}
		preview := newPreview("reinstall_update", "Reinstall/update selected skill", program, args, "Reinstall/update this skill via the official skills CLI after confirmation.", true, true, false, "yes")
		previews = append(previews, preview)
	} else {
		previews = append(previews, unavailablePreview("Reinstall/update selected skill", reason))
	}

	if target, ok, reason := removeIdentity(sk); ok {
		program, baseArgs := resolve()
		args := append([]string{}, baseArgs...)
		args = append(args, "remove", target, "--yes")
		if sk.Scope == model.ScopeGlobal {
			args = append(args, "-g")
		}
		previews = append(previews, newPreview("remove", "Remove selected skill", program, args, "Remove this installed skill via the official skills CLI after typing the exact target.", true, true, true, target))
	} else {
		previews = append(previews, unavailablePreview("Remove selected skill", reason))
	}
	return previews
}

func openEditorAction(sk *model.Skill) (CommandPreview, bool, string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return CommandPreview{}, false, "$EDITOR is not set"
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 || !safeEditorToken(parts[0]) {
		return CommandPreview{}, false, "$EDITOR is empty or unsafe"
	}
	for _, arg := range parts[1:] {
		if !safeEditorArg(arg) {
			return CommandPreview{}, false, "$EDITOR arguments are unsafe"
		}
	}
	target := firstRawNonEmpty(sk.SkillPath, sk.CanonicalPath)
	if target == "" {
		return CommandPreview{}, false, "skill path is unavailable"
	}
	if !safeExecValue(target) {
		return CommandPreview{}, false, "skill path contains unsafe characters"
	}
	args := append([]string{}, parts[1:]...)
	args = append(args, target)
	preview := newPreview("open_skill", "Open selected skill", parts[0], args, "Open this skill in $EDITOR. LazySkills releases the terminal while the editor runs.", false, false, false, "")
	if !preview.Available {
		return CommandPreview{}, false, preview.Reason
	}
	preview.Exec.Interactive = true
	return preview, true, ""
}

func ResolveSkillsCommand() (string, []string) {
	if _, err := exec.LookPath("skills"); err == nil {
		return "skills", nil
	}
	return "npx", []string{"--yes", "skills"}
}

func newPreview(id, title, program string, args []string, description string, mutates, confirm, dangerous bool, confirmValue string) CommandPreview {
	if !safeExecValue(program) {
		return unavailablePreview(title, "program is empty or unsafe")
	}
	execArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if !safeExecValue(arg) {
			return unavailablePreview(title, "command argument is empty, option-like, or contains control characters")
		}
		execArgs = append(execArgs, arg)
	}
	preview := CommandPreview{ID: id, Title: title, Program: program, Args: execArgs, Exec: ExecSpec{Program: program, Args: execArgs}, Description: description, Mutates: mutates, RequiresConfirm: confirm, Dangerous: dangerous, ConfirmValue: confirmValue, Available: true}
	preview.Command = renderCommand(program, execArgs)
	return preview
}

func unavailablePreview(title, reason string) CommandPreview {
	return CommandPreview{Title: title, Available: false, Reason: compat.SanitizeMetadata(firstNonEmpty(reason, "not enough safe identity data to build this command"))}
}

func addIdentity(sk *model.Skill) (source string, skillFilter string, ok bool, reason string) {
	source, ref, skillPath := sourceRefPath(sk)
	source = buildInstallSource(source, ref, skillPath)
	if !safeExecValue(source) || strings.HasPrefix(source, "-") {
		return "", "", false, "source is empty or option-like"
	}
	filter := sk.Name
	if !safeExecValue(filter) || strings.HasPrefix(filter, "-") {
		return "", "", false, "skill name is empty or option-like"
	}
	return source, filter, true, ""
}

func removeIdentity(sk *model.Skill) (target string, ok bool, reason string) {
	for _, path := range candidateInstallPaths(sk) {
		base := filepath.Base(path)
		if safeExecValue(base) && !strings.HasPrefix(base, "-") {
			return base, true, ""
		}
	}
	return "", false, "installed directory identity is empty or option-like"
}

func sourceRefPath(sk *model.Skill) (source, ref, skillPath string) {
	if sk.Scope == model.ScopeProject && sk.LocalLock != nil {
		return sk.LocalLock.Source, sk.LocalLock.Ref, sk.LocalLock.SkillPath
	}
	if sk.Scope == model.ScopeGlobal && sk.GlobalLock != nil {
		return globalUpdateSource(*sk.GlobalLock), sk.GlobalLock.Ref, sk.GlobalLock.SkillPath
	}
	if sk.LocalLock != nil {
		return sk.LocalLock.Source, sk.LocalLock.Ref, sk.LocalLock.SkillPath
	}
	if sk.GlobalLock != nil {
		return globalUpdateSource(*sk.GlobalLock), sk.GlobalLock.Ref, sk.GlobalLock.SkillPath
	}
	return "", "", ""
}

func globalUpdateSource(entry model.GlobalLockEntry) string {
	if entry.SkillPath == "" {
		return firstRawNonEmpty(entry.SourceURL, entry.Source)
	}
	return firstRawNonEmpty(entry.Source, entry.SourceURL)
}

func buildInstallSource(source, ref, skillPath string) string {
	if source == "" {
		return ""
	}
	if !safeExecValue(source) || strings.HasPrefix(source, "-") {
		return ""
	}
	if ref != "" && (!safeExecValue(ref) || strings.HasPrefix(ref, "-")) {
		return ""
	}
	if skillPath != "" && (!safeExecValue(skillPath) || strings.HasPrefix(skillPath, "-")) {
		return ""
	}
	if skillPath != "" && supportsAppendedSubpath(source) {
		folder := deriveSkillFolder(skillPath)
		if folder != "" {
			source = strings.TrimRight(source, "/") + "/" + folder
		}
	}
	if ref != "" {
		source += "#" + ref
	}
	return source
}

func deriveSkillFolder(skillPath string) string {
	folder := skillPath
	if strings.HasSuffix(folder, "/SKILL.md") {
		folder = strings.TrimSuffix(folder, "/SKILL.md")
	} else if strings.HasSuffix(folder, "SKILL.md") {
		folder = strings.TrimSuffix(folder, "SKILL.md")
	}
	return strings.Trim(folder, "/")
}

func supportsAppendedSubpath(source string) bool {
	if strings.HasPrefix(source, "git@") || strings.HasSuffix(source, ".git") {
		return false
	}
	if strings.HasPrefix(source, "https://github.com/") || strings.HasPrefix(source, "https://gitlab.com/") || !strings.Contains(source, "://") {
		return true
	}
	return false
}

func candidateInstallPaths(sk *model.Skill) []string {
	paths := []string{}
	if sk.CanonicalPath != "" {
		paths = append(paths, sk.CanonicalPath)
	}
	for _, observed := range sk.ObservedPaths {
		if observed.Path != "" {
			paths = append(paths, observed.Path)
		}
	}
	return paths
}

func safeCLIValue(value string) bool {
	value = compat.SanitizeMetadata(value)
	return value != "" && !strings.HasPrefix(value, "-")
}

func safeExecValue(value string) bool {
	if value == "" {
		return false
	}
	return compat.SanitizeMetadata(value) == value && !strings.ContainsAny(value, "\x00\x1b\r\n")
}

func safeEditorToken(value string) bool {
	return safeExecValue(value) && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "'\"$`\\!*?[]{}()&;<>|")
}

func safeEditorArg(value string) bool {
	return safeExecValue(value) && !strings.ContainsAny(value, "'\"$`\\!*?[]{}()&;<>|")
}

func safeToken(value string) string {
	value = compat.SanitizeMetadata(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\n'\"$`\\!*?[]{}()&;<>|") {
		return ""
	}
	return value
}

func renderCommand(program string, args []string) string {
	parts := []string{shellQuote(program)}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	value = compat.SanitizeMetadata(value)
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n'\"$`\\!*?[]{}()&;<>|#") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return compat.SanitizeMetadata(value)
		}
	}
	return ""
}

func firstRawNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
