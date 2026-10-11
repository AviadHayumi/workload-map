// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

const { PILOT_BASE_BRANCH, PILOT_RUN } = process.env;
if (PILOT_BASE_BRANCH !== 'codex/e2e-renovate-pilot' || !/^\d+-\d+$/.test(PILOT_RUN || '')) {
  throw new Error('Set the fork pilot branch and a unique GitHub run-attempt identity');
}

module.exports = {
  platform: 'github',
  repositories: ['AviadHayumi/workload-map'],
  onboarding: false,
  requireConfig: 'ignored',
  forkProcessing: 'enabled',
  enabledManagers: ['custom.regex'],
  includePaths: ['hack/e2e/global.env'],
  baseBranchPatterns: [PILOT_BASE_BRANCH],
  branchPrefix: `codex/renovate-pilot/${PILOT_RUN}/`,
  additionalBranchPrefix: '',
  branchTopic: 'jobset',
  // A fresh branch has no successful checks. The pilot publishes a PR after recording.
  prCreation: 'status-success',
  dependencyDashboard: false,
  dependencyDashboardTitle: 'Karta operator pilot dependency dashboard',
  pruneStaleBranches: false,
  automerge: false,
  ignoreTests: false,
  ignoreUnstable: true,
  username: 'github-actions[bot]',
  gitAuthor: 'github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>',
  semanticCommits: 'enabled',
  semanticCommitType: 'build',
  semanticCommitScope: 'e2e',
  commitBody: 'Signed-off-by: github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>',
  customManagers: [{
    customType: 'regex',
    managerFilePatterns: ['/^hack\\/e2e\\/global\\.env$/'],
    matchStrings: ['(?:^|\\n)JOBSET_VERSION="\\$\\{JOBSET_VERSION:-(?<currentValue>v[0-9]+\\.[0-9]+\\.[0-9]+)\\}"'],
    depNameTemplate: 'kubernetes-sigs/jobset',
    datasourceTemplate: 'github-releases',
    versioningTemplate: 'semver',
  }],
  packageRules: [{
    matchPackageNames: ['kubernetes-sigs/jobset'],
    // v0.12.0 explicitly tests Kubernetes 1.34; expand only after another review.
    allowedVersions: '>=0.10.1 <=0.12.0',
  }],
};
