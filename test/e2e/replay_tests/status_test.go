// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package replay

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	kartav1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

const (
	repoRoot     = "../../.."
	recordedGlob = "../recorded_data/*/*/*/*.yaml"
)

// Historical recordings retain their original labels. Migration expectations pin
// those exact inputs and assert every status in the new catalog's output.
type migrationExpectation struct {
	SHA256   string                           `json:"sha256"`
	Statuses [][]kartav1alpha1.ResourceStatus `json:"statuses"`
}

// Each recording is a real flow the recorder captured. Walk it step by step through the recording reader:
// Karta must parse every CR, and the statuses it reads must include the state the recorder observed from
// the CR's own fields.
var _ = Describe("Karta reads the recorded state", func() {
	recordings, globErr := filepath.Glob(recordedGlob)
	var migration struct {
		Recordings map[string]migrationExpectation `json:"recordings"`
	}
	migrationData, readErr := os.ReadFile("testdata/status_migration.json")
	decodeErr := json.Unmarshal(migrationData, &migration)
	It("has valid historical migration expectations", func() {
		Expect(globErr).NotTo(HaveOccurred())
		Expect(readErr).NotTo(HaveOccurred())
		Expect(decodeErr).NotTo(HaveOccurred())
		for name := range migration.Recordings {
			Expect(recordings).To(ContainElement(filepath.Join("../recorded_data", name)), "orphan migration entry %s", name)
		}
	})
	if len(recordings) == 0 {
		It("has recordings to replay", func() {
			Fail("no recordings under test/e2e/recorded_data; run make record-e2e")
		})
		return
	}

	for _, path := range recordings {
		path := path
		It(strings.TrimPrefix(path, "../recorded_data/"), func(ctx SpecContext) {
			Expect(readErr).NotTo(HaveOccurred())
			Expect(decodeErr).NotTo(HaveOccurred())
			expected, migrated := migration.Recordings[strings.TrimPrefix(path, "../recorded_data/")]
			if migrated {
				data, err := os.ReadFile(path)
				Expect(err).NotTo(HaveOccurred())
				Expect(fmt.Sprintf("%x", sha256.Sum256(data))).To(Equal(expected.SHA256),
					"historical input changed; remove its migration entry after a live re-recording")
			}
			r, err := recorder.OpenRecording(path)
			Expect(err).NotTo(HaveOccurred())

			name := filepath.Base(r.Recording().KartaFile)
			Expect(name).To(MatchRegexp(`^[a-zA-Z0-9._-]+\.yaml$`),
				"recording %q names a suspicious KartaFile %q", path, r.Recording().KartaFile)
			kartaYAML, err := os.ReadFile(filepath.Join(repoRoot, "docs", "catalog", name))
			Expect(err).NotTo(HaveOccurred())
			karta := &kartav1alpha1.Karta{}
			Expect(yaml.Unmarshal(kartaYAML, karta)).To(Succeed())

			frame := 0
			for r.Next() {
				state, cr := r.State(), r.Object()
				root, err := resource.NewComponentFactoryFromObject(karta, cr).GetRootComponent()
				Expect(err).NotTo(HaveOccurred(), "Karta could not parse the %q CR", state)
				status, err := root.GetStatus(ctx)
				Expect(err).NotTo(HaveOccurred(), "Karta could not read the %q CR", state)
				Expect(status).NotTo(BeNil(), "Karta read no status from the %q CR", state)
				if migrated {
					Expect(frame).To(BeNumerically("<", len(expected.Statuses)), "missing migration expectation for frame %d", frame)
					Expect(expected.Statuses[frame]).NotTo(BeEmpty())
					Expect(status.MatchedStatuses).To(ConsistOf(expected.Statuses[frame]),
						"frame %d: Karta read %v; historical recorded label was %q", frame, status.MatchedStatuses, state)
				} else {
					Expect(status.MatchedStatuses).To(ContainElement(kartav1alpha1.ResourceStatus(state)),
						"Karta read %v, recorded state was %q", status.MatchedStatuses, state)
				}
				frame++
			}
			if migrated {
				Expect(frame).To(Equal(len(expected.Statuses)), "unused migration expectations")
			}
		})
	}
})
