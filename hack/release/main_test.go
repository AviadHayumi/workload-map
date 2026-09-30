// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Release validation", func() {
	Describe("module versions", func() {
		It("accepts synchronized inline requirements", func() {
			root := GinkgoT().TempDir()
			writeModuleFiles(root, "require "+rootModule+" v1.2.3\n")
			Expect(validateModuleVersions(root, "1.2.3")).To(Succeed())
		})

		It("accepts synchronized block requirements", func() {
			root := GinkgoT().TempDir()
			writeModuleFiles(root, "require (\n\t"+rootModule+" v1.2.3\n)\n")
			Expect(validateModuleVersions(root, "1.2.3")).To(Succeed())
		})

		It("validates modules from an explicit repository root", func() {
			root := GinkgoT().TempDir()
			writeModuleFiles(root, "require "+rootModule+" v1.2.3\n")
			Expect(runValidateVersion([]string{"--root", root, "--version", "1.2.3"})).To(Succeed())
		})

		It("rejects a stale workspace replacement", func() {
			root := GinkgoT().TempDir()
			writeModuleFiles(root, "require "+rootModule+" v1.2.3\n")
			writeWorkspace(root, "v1.2.2")
			Expect(validateModuleVersions(root, "1.2.3")).To(MatchError(ContainSubstring("go.work must replace")))
		})

		DescribeTable("rejects publication-unsafe module files",
			func(body, message string) {
				root := GinkgoT().TempDir()
				writeModuleFiles(root, body)
				Expect(validateModuleVersions(root, "1.2.3")).To(MatchError(ContainSubstring(message)))
			},
			Entry("mismatched root version",
				"require "+rootModule+" v1.2.2\n", "v1.2.2, want v1.2.3"),
			Entry("single-line replacement",
				"require "+rootModule+" v1.2.3\nreplace "+rootModule+" => ../\n", "replace directive"),
			Entry("block replacement after the requirement",
				"require "+rootModule+" v1.2.3\nreplace (\n\t"+rootModule+" => ../\n)\n", "replace directive"),
			Entry("block replacement before the requirement",
				"replace (\n\t"+rootModule+" => ../\n)\nrequire "+rootModule+" v1.2.3\n", "replace directive"),
			Entry("exclusion",
				"exclude "+rootModule+" v1.2.2\nrequire "+rootModule+" v1.2.3\n", "exclude directive"),
		)
	})

	It("validates synchronized tags from an explicit repository root", func() {
		root := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(root, "tracked"), []byte("contents"), 0o644)).To(Succeed())
		commands := [][]string{
			{"init"},
			{"add", "tracked"},
			{"-c", "user.name=Karta Test", "-c", "user.email=karta@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "test"},
			{"-c", "tag.gpgSign=false", "tag", "v1.2.3"},
			{"-c", "tag.gpgSign=false", "tag", "cli/v1.2.3"},
		}
		for _, args := range commands {
			_, err := commandOutput("git", append([]string{"-C", root}, args...)...)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(validateTags(root, "1.2.3")).To(Succeed())
	})

	It("rejects a version that has already been released", func() {
		root := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(root, "tracked"), []byte("contents"), 0o644)).To(Succeed())
		commands := [][]string{
			{"init"},
			{"add", "tracked"},
			{"-c", "user.name=Karta Test", "-c", "user.email=karta@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "released"},
			{"-c", "tag.gpgSign=false", "tag", "v1.2.3"},
			{"-c", "tag.gpgSign=false", "tag", "v1.2.4-rc.1"},
		}
		for _, args := range commands {
			_, err := commandOutput("git", append([]string{"-C", root}, args...)...)
			Expect(err).NotTo(HaveOccurred())
		}

		// A second commit so the released tag is behind HEAD, as it is on a
		// branch that has not been tagged yet.
		Expect(os.WriteFile(filepath.Join(root, "tracked"), []byte("more"), 0o644)).To(Succeed())
		for _, args := range [][]string{
			{"add", "tracked"},
			{"-c", "user.name=Karta Test", "-c", "user.email=karta@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "next"},
		} {
			_, err := commandOutput("git", append([]string{"-C", root}, args...)...)
			Expect(err).NotTo(HaveOccurred())
		}

		Expect(validateVersionIsUnreleased(root, "1.2.4")).To(Succeed())
		Expect(validateVersionIsUnreleased(root, "1.2.3")).To(
			MatchError(ContainSubstring("not newer than the released v1.2.3")))
		Expect(validateVersionIsUnreleased(root, "1.2.2")).To(
			MatchError(ContainSubstring("not newer than the released v1.2.3")))

		// The tag being cut sits at HEAD during the release itself, so it must
		// not count as already released.
		_, err := commandOutput("git", "-C", root, "-c", "tag.gpgSign=false", "tag", "v1.2.4")
		Expect(err).NotTo(HaveOccurred())
		Expect(validateVersionIsUnreleased(root, "1.2.4")).To(Succeed())
	})

	It("resolves GoReleaser artifact paths from the project root", func() {
		root := GinkgoT().TempDir()
		dist := filepath.Join(root, "dist")
		Expect(os.MkdirAll(dist, 0o755)).To(Succeed())
		contents := `[{"name":"karta","path":"dist/karta_linux_amd64/karta","type":"Binary"}]`
		Expect(os.WriteFile(filepath.Join(dist, "artifacts.json"), []byte(contents), 0o644)).To(Succeed())

		artifacts, err := readArtifacts(dist)
		Expect(err).NotTo(HaveOccurred())
		Expect(artifacts).To(HaveLen(1))
		Expect(artifacts[0].Path).To(Equal(filepath.Join(dist, "karta_linux_amd64", "karta")))
	})

	It("verifies host executable versions", func() {
		path := filepath.Join(GinkgoT().TempDir(), "version-command")
		Expect(os.WriteFile(path, []byte("#!/bin/sh\nprintf '1.2.3\\n'\n"), 0o755)).To(Succeed())
		artifacts := []artifact{
			{Path: path, Goos: runtime.GOOS, Goarch: runtime.GOARCH, Type: "Binary", Extra: map[string]any{"ID": "karta"}},
		}
		verified, skipped, err := verifyHostVersions(artifacts, "1.2.3")
		Expect(err).NotTo(HaveOccurred())
		Expect(verified).To(Equal([]string{"karta"}))
		Expect(skipped).To(BeEmpty())

		_, _, err = verifyHostVersions(artifacts, "1.2.4")
		Expect(err).To(MatchError(ContainSubstring("want \"1.2.4\"")))

	})
})

func writeModuleFiles(root, body string) {
	GinkgoHelper()
	for _, module := range []string{"cli", "operator"} {
		Expect(os.MkdirAll(filepath.Join(root, module), 0o755)).To(Succeed())
		contents := "module " + rootModule + "/" + module + "\n\ngo 1.26.3\n\n" + body
		Expect(os.WriteFile(filepath.Join(root, module, "go.mod"), []byte(contents), 0o644)).To(Succeed())
	}
	writeWorkspace(root, "v1.2.3")
}

func writeWorkspace(root, version string) {
	GinkgoHelper()
	contents := "go 1.26.3\n\nreplace " + rootModule + " " + version + " => .\n"
	Expect(os.WriteFile(filepath.Join(root, "go.work"), []byte(contents), 0o644)).To(Succeed())
}
