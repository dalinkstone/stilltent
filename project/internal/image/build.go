// Package image provides image pipeline functionality.
// This file implements Tentfile parsing and image building.
// A Tentfile is a simple Dockerfile-like format for building custom sandbox images.
//
// Supported instructions:
//   - FROM <base-image>        — base image (required, must be first)
//   - RUN <command>            — execute a shell command in the build context
//   - COPY <src> <dst>         — copy files from host to image
//   - ENV <key>=<value>        — set environment variable
//   - WORKDIR <path>           — set working directory for subsequent RUN commands
//   - EXPOSE <port>            — document exposed ports (metadata only)
//   - LABEL <key>=<value>      — add metadata label
package image

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Instruction represents a single Tentfile instruction.
type Instruction struct {
	// Command is the instruction keyword (FROM, RUN, COPY, etc.)
	Command string
	// Args is the raw argument string
	Args string
	// Line is the source line number (for error reporting)
	Line int
}

// Tentfile represents a parsed Tentfile.
type Tentfile struct {
	// BaseImage is the FROM reference
	BaseImage string
	// Instructions is the ordered list of build instructions (excluding FROM)
	Instructions []Instruction
	// Labels are metadata key=value pairs from LABEL instructions
	Labels map[string]string
	// ExposedPorts are ports declared with EXPOSE
	ExposedPorts []string
	// EnvVars are environment variables set with ENV
	EnvVars map[string]string
}

// ParseTentfile reads and parses a Tentfile from the given path.
func ParseTentfile(path string) (*Tentfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open Tentfile: %w", err)
	}
	defer f.Close()

	tf := &Tentfile{
		Labels:  make(map[string]string),
		EnvVars: make(map[string]string),
	}

	scanner := bufio.NewScanner(f)
	lineNum := 0
	var continuation string

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Handle line continuation with backslash
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			continuation += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		if continuation != "" {
			line = continuation + line
			continuation = ""
		}

		// Strip comments and whitespace
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split into command and args
		parts := strings.SplitN(line, " ", 2)
		cmd := strings.ToUpper(parts[0])
		args := ""
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}

		switch cmd {
		case "FROM":
			if args == "" {
				return nil, fmt.Errorf("line %d: FROM requires an image reference", lineNum)
			}
			if tf.BaseImage != "" {
				return nil, fmt.Errorf("line %d: multiple FROM instructions not supported", lineNum)
			}
			tf.BaseImage = args

		case "RUN":
			if args == "" {
				return nil, fmt.Errorf("line %d: RUN requires a command", lineNum)
			}
			tf.Instructions = append(tf.Instructions, Instruction{
				Command: "RUN",
				Args:    args,
				Line:    lineNum,
			})

		case "COPY":
			if args == "" {
				return nil, fmt.Errorf("line %d: COPY requires source and destination", lineNum)
			}
			copyParts := splitCopyArgs(args)
			if len(copyParts) < 2 {
				return nil, fmt.Errorf("line %d: COPY requires source and destination paths", lineNum)
			}
			tf.Instructions = append(tf.Instructions, Instruction{
				Command: "COPY",
				Args:    args,
				Line:    lineNum,
			})

		case "ENV":
			if args == "" {
				return nil, fmt.Errorf("line %d: ENV requires key=value", lineNum)
			}
			key, value := parseEnvArg(args)
			if key == "" {
				return nil, fmt.Errorf("line %d: invalid ENV format, expected KEY=VALUE or KEY VALUE", lineNum)
			}
			tf.EnvVars[key] = value
			tf.Instructions = append(tf.Instructions, Instruction{
				Command: "ENV",
				Args:    args,
				Line:    lineNum,
			})

		case "WORKDIR":
			if args == "" {
				return nil, fmt.Errorf("line %d: WORKDIR requires a path", lineNum)
			}
			tf.Instructions = append(tf.Instructions, Instruction{
				Command: "WORKDIR",
				Args:    args,
				Line:    lineNum,
			})

		case "EXPOSE":
			if args == "" {
				return nil, fmt.Errorf("line %d: EXPOSE requires a port", lineNum)
			}
			tf.ExposedPorts = append(tf.ExposedPorts, args)
			tf.Instructions = append(tf.Instructions, Instruction{
				Command: "EXPOSE",
				Args:    args,
				Line:    lineNum,
			})

		case "LABEL":
			if args == "" {
				return nil, fmt.Errorf("line %d: LABEL requires key=value", lineNum)
			}
			key, value := parseEnvArg(args)
			if key != "" {
				tf.Labels[key] = value
			}

		default:
			return nil, fmt.Errorf("line %d: unknown instruction %q", lineNum, cmd)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading Tentfile: %w", err)
	}

	if tf.BaseImage == "" {
		return nil, fmt.Errorf("Tentfile must contain a FROM instruction")
	}

	return tf, nil
}

// BuildResult holds the result of an image build.
type BuildResult struct {
	// ImageName is the name of the built image
	ImageName string
	// ImagePath is the path to the image file
	ImagePath string
	// BaseImage is the FROM reference
	BaseImage string
	// Steps is the total number of build steps executed
	Steps int
	// Duration is how long the build took
	Duration time.Duration
	// Labels are metadata labels
	Labels map[string]string
}

// BuildImage builds a custom image from a Tentfile.
// It resolves the base image, creates a build context directory, applies
// instructions (COPY, ENV, WORKDIR produce filesystem artifacts; RUN
// instructions are recorded as a build script for execution inside the
// sandbox on first boot), and produces the final image.
func (m *Manager) BuildImage(name string, tentfilePath string) (*BuildResult, error) {
	start := time.Now()

	// Parse the Tentfile
	tf, err := ParseTentfile(tentfilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Tentfile: %w", err)
	}

	// Resolve the base image
	basePath, err := m.ResolveImage(tf.BaseImage)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base image %q: %w", tf.BaseImage, err)
	}

	// Create images directory
	if err := os.MkdirAll(m.baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create images directory: %w", err)
	}

	// Create build context directory
	buildDir := filepath.Join(m.baseDir, fmt.Sprintf("%s-build", name))
	os.RemoveAll(buildDir)
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create build directory: %w", err)
	}
	defer os.RemoveAll(buildDir)

	// Copy base image to target
	targetPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.img", name))
	if err := m.copyFile(basePath, targetPath); err != nil {
		return nil, fmt.Errorf("failed to copy base image: %w", err)
	}

	// Create rootfs overlay directory for build artifacts
	rootfsDir := filepath.Join(m.baseDir, name+"_rootfs")
	os.RemoveAll(rootfsDir)
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create rootfs directory: %w", err)
	}

	// Process instructions
	tentfileDir := filepath.Dir(tentfilePath)
	if !filepath.IsAbs(tentfileDir) {
		abs, err := filepath.Abs(tentfileDir)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve Tentfile directory: %w", err)
		}
		tentfileDir = abs
	}

	steps := 0
	var runCommands []string
	var envLines []string
	workdir := "/"

	for _, inst := range tf.Instructions {
		steps++
		switch inst.Command {
		case "COPY":
			parts := splitCopyArgs(inst.Args)
			src := parts[0]
			dst := parts[1]

			// Resolve source relative to Tentfile directory
			if !filepath.IsAbs(src) {
				src = filepath.Join(tentfileDir, src)
			}
			// Resolve destination in rootfs overlay
			dstFull := filepath.Join(rootfsDir, dst)

			if err := copyToRootfs(src, dstFull); err != nil {
				return nil, fmt.Errorf("step %d (COPY): %w", steps, err)
			}

		case "RUN":
			// Record RUN commands for execution inside sandbox on first boot
			runCommands = append(runCommands, inst.Args)

		case "ENV":
			key, value := parseEnvArg(inst.Args)
			envLines = append(envLines, fmt.Sprintf("export %s=%q", key, value))

		case "WORKDIR":
			workdir = inst.Args
			// Create the directory in rootfs overlay
			dirPath := filepath.Join(rootfsDir, workdir)
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				return nil, fmt.Errorf("step %d (WORKDIR): failed to create directory: %w", steps, err)
			}

		case "EXPOSE":
			// Metadata only — no filesystem action needed
		}
	}

	// Write build script if there are RUN commands
	if len(runCommands) > 0 || len(envLines) > 0 {
		scriptDir := filepath.Join(rootfsDir, "etc", "tent")
		if err := os.MkdirAll(scriptDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create build script directory: %w", err)
		}

		var script strings.Builder
		script.WriteString("#!/bin/sh\n")
		script.WriteString("# Generated by tent image build\n")
		script.WriteString("set -e\n\n")

		// Write environment variables
		for _, env := range envLines {
			script.WriteString(env + "\n")
		}
		if len(envLines) > 0 {
			script.WriteString("\n")
		}

		// Write workdir
		if workdir != "/" {
			script.WriteString(fmt.Sprintf("cd %s\n\n", workdir))
		}

		// Write RUN commands
		for _, cmd := range runCommands {
			script.WriteString(cmd + "\n")
		}

		scriptPath := filepath.Join(scriptDir, "build.sh")
		if err := os.WriteFile(scriptPath, []byte(script.String()), 0755); err != nil {
			return nil, fmt.Errorf("failed to write build script: %w", err)
		}
	}

	// Write build metadata
	metaDir := filepath.Join(rootfsDir, "etc", "tent")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create metadata directory: %w", err)
	}

	var meta strings.Builder
	meta.WriteString(fmt.Sprintf("name=%s\n", name))
	meta.WriteString(fmt.Sprintf("base=%s\n", tf.BaseImage))
	meta.WriteString(fmt.Sprintf("built=%s\n", time.Now().Format(time.RFC3339)))
	for k, v := range tf.Labels {
		meta.WriteString(fmt.Sprintf("label.%s=%s\n", k, v))
	}
	for _, p := range tf.ExposedPorts {
		meta.WriteString(fmt.Sprintf("expose=%s\n", p))
	}

	metaPath := filepath.Join(metaDir, "image.meta")
	if err := os.WriteFile(metaPath, []byte(meta.String()), 0644); err != nil {
		return nil, fmt.Errorf("failed to write image metadata: %w", err)
	}

	return &BuildResult{
		ImageName: name,
		ImagePath: targetPath,
		BaseImage: tf.BaseImage,
		Steps:     steps,
		Duration:  time.Since(start),
		Labels:    tf.Labels,
	}, nil
}

// splitCopyArgs splits COPY arguments, handling quoted paths.
func splitCopyArgs(args string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(args); i++ {
		c := args[i]
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
			continue
		}
		if inQuote && c == quoteChar {
			inQuote = false
			continue
		}
		if !inQuote && c == ' ' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteByte(c)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// parseEnvArg parses "KEY=VALUE" or "KEY VALUE" format.
func parseEnvArg(args string) (string, string) {
	// Try KEY=VALUE first
	if idx := strings.Index(args, "="); idx > 0 {
		key := strings.TrimSpace(args[:idx])
		value := strings.TrimSpace(args[idx+1:])
		// Strip surrounding quotes from value
		value = strings.Trim(value, "\"'")
		return key, value
	}

	// Try KEY VALUE
	parts := strings.SplitN(args, " ", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}

	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}

	return "", ""
}

// copyToRootfs copies a file or directory from host to the rootfs overlay.
func copyToRootfs(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("source not found: %w", err)
	}

	if srcInfo.IsDir() {
		return copyDir(src, dst)
	}
	return copySingleFile(src, dst)
}

// copyDir recursively copies a directory.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copySingleFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copySingleFile copies a single file, preserving permissions.
func copySingleFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = dstFile.ReadFrom(srcFile)
	return err
}
