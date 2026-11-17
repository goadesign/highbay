#!/usr/bin/env bash
# Run the native process and filesystem contracts on integration PRs.
# Full native ordinary coverage has its separate push/nightly/manual owners.
set -euo pipefail
exec </dev/null

if [ "$#" -ne 0 ]; then
  echo 'usage: test-native.sh' >&2
  exit 1
fi
repository=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repository"

# Keep instrumentation with the existing owner and reject an empty test batch.
run_tests() {
  local pattern=$1
  shift
  bash "$repository/.github/scripts/go-test.sh" -vet=all -count=1 -timeout 20m -json \
    -run "$pattern" -- "$@" </dev/null \
    | go run "$repository/.github/scripts/testcheck/main.go"
}

# Exact names distinguish native boundary cases from portable logic in a package.
run_selected() {
  local package=$1
  shift
  local names
  names=$(IFS='|'; printf '%s' "$*")
  run_tests "^($names)$" "$package"
}

# These complete resource owners exercise Darwin syscalls, executable files,
# process groups, signals, descriptor paths, atomic rename and private storage.
run_tests . \
  ./internal/abspath \
  ./internal/renameat \
  ./internal/procgroup \
  ./internal/genpublish \
  ./internal/traceview \
  ./internal/verifyrun \
  ./verify/candidate \
  ./verify/suite

# Descriptor-relative copy/publication, aliases, locks, durability and live/helper lifetime.
run_selected ./internal/controlplane \
  TestOpenEntryHandlesRegularFileDirectorySymlinkAndFIFO \
  TestCopyGrantExcludesProtectedDirectoryAliases \
  TestCopyEntryDecidesProtectedAliasWhenItOpensTheEntry \
  TestCopyGrantCopiesLiveTree \
  TestCopyGrantWorkspaceInsideMonorepo \
  TestCopyEntryReportsVanishedEntry \
  TestCopyGrantStopsWhenTheOperationEnds \
  TestCopyGrantRefusesAReplacedNestedWorkspace \
  TestCopyGrantTreatsSymlinkedGoModAsAModule \
  TestCheckBindingAcceptsOtherSpellingsThroughTheStartupPipeline \
  TestPublishManifest \
  TestPublishManifestRefusesLiveSymlinkAndKeepsItsTarget \
  TestPublishManifestWritesThroughTheHeldGrant \
  TestPublishManifestWritesThroughTheVerifiedNestedWorkspace \
  TestOpenLiveUsesTheSourceRootsAcceptedAtStartup \
  TestOpenLiveRefusesAWorkspaceReparentedBeforeBinding \
  TestPublishManifestRefusesWhileAnotherWriterHoldsTheDirectory \
  TestPublishManifestRefusesALinkedGoModInTheCopy \
  TestPublishManifestsReportsAWrittenFileWhoseSyncFailed \
  TestPublishManifestsStopsBeforeEachFileOnceCanceled \
  TestNewRejectsAliasedWorkspaceStorage \
  TestInitProjectAcceptsAnotherSpellingOfTheWorkspace \
  TestBoundControlPlaneDefaultEvaluatorAndGeneratorIgnoreWorkspaceTMPDIRAndCleanOwnWork \
  TestNewAndInitProjectNormalizeSymlinkBeforeParentPaths \
  TestSyncDirTreeFDFollowedByRemoveOwnedStagingDirAndFailedWALCleanup \
  TestNewAndInitProjectPreserveSignificantPathWhitespace \
  TestNewProtectsMonorepoAndPreservesValidLayouts \
  TestWorkspaceWALReconciliationStates \
  TestBuildDesignDoesNotWriteThroughHardLinks \
  TestBuildDesignRefusesLinksAndNestedModules \
  TestStagingDoesNotFollowLinks \
  TestCardStoreCloseReleasesDuplicatedLock \
  TestDesignVersionRefusesLinks \
  TestDesignVersionAndRecordFIFOStaging \
  TestDraftTakeWaitsForDirectoryDurability \
  TestAFailedStopKeepsTheOwnerAndStartsNoOtherTarget \
  TestAStartThatFailsBeforeReadyAndCannotBeCleanedUpKeepsItsOwner \
  TestGetLiveTargetShowsATargetNotConfirmedStopped \
  TestLiveTargetAppliesDraftsThroughTheGeneratedContract \
  TestLiveTargetThatEndsIsUnboundAndCleanedUp \
  TestLiveTargetReplacementNeverReachesTwoTargets \
  TestCloseEndsAStartInProgressAndRefusesLaterStarts \
  TestAStartThatTimesOutSaysSo \
  TestCheckHelperFinishesTheOperationWhenItsServerIsKilled \
  TestNewSkipsAnOperationWhoseHelperStillHoldsItsLock \
  TestRecoveryAfterACrashInEachWindow \
  TestOwnerRecordIsWrittenBeforeEachStep \
  TestCheckModuleCacheAdmission

# Project process start/attach/stop, signal survival, private paths and native command launch.
run_selected ./cmd/surla \
  TestCommandErrorsNeverIncludeArguments \
  TestConfigureServeWorkspaceBindsTheNamedProject \
  TestConfigureServeWorkspaceKeepsHistoryAcrossSpellingsOfTheWorkspace \
  TestDefaultOperatorRootDeterministicAndPermissionValidation \
  TestDetachedTeamStartersSurvivePollingDeadline \
  TestInitConsidersTheCompleteLocationBeforeSelecting \
  TestInitUsesTemporaryStorageOnTheDestinationDevice \
  TestLeftoverLiveFoldersAreNamedAndKept \
  TestLocalWorkSkipsDirectoriesWithoutRequiredPermissions \
  TestLocalWorkStaysOutsideStartupAndTargets \
  TestManagementConnectionIsLocalAndProxyFree \
  TestMissingBrowserOpenerFailsBeforeProjectCreation \
  TestPreviewAndDiffRemoveTheirWorkspaceWhenEvaluationFails \
  TestProjectListenerRevokesBeforeClose \
  TestProjectSettingsRejectsWorkspaceAndRepositoryPaths \
  TestProjectStartLeavesLiveTerminalUnread \
  TestProjectStartPortTakeoverRemovesOnlyFailedTerminal \
  TestProjectStartPreflightPrecedesState \
  TestProjectStartRefusesTakenPortAndHeldUnansweringStore \
  TestProjectStartSerializesAndRecovers \
  TestProjectStartShowsEndedServerOutput \
  TestProjectStartThatAnswersReadsNothing \
  TestProjectTerminalLookupUsesOnlyInjectedProgram \
  TestProtectedDirectoryRejectsCaseAliases \
  TestProtectedDirectoryRejectsWorkspaceAliases \
  TestReadModuleSnapshotConfinesReadsToOpenedRoot \
  TestReadModuleSnapshotReadsOnlyTheModulesOwnRegularFiles \
  TestReadModuleSnapshotRejectsSymbolicLinks \
  TestReadProjectTerminalArguments \
  TestReleaseTargetMeasuresStagedBinaries \
  TestReleaseTargetRefusesNonRegularFiles \
  TestRunInitKeepsFailedLogsOutsideTheWorkspace \
  TestRuntimeRootsAreIndependent \
  TestServeEntryRemovesProviderFamiliesAndKeepsHerdrsCopy \
  TestServeOptionsBindsTheCheckHelperOnEveryServePath \
  TestServeOptionsStartLiveTargetsWithoutAModel \
  TestServeProcessKeepsProviderValuesOutOfItsOutputAndFiles \
  TestServeProposesWithoutAModelAndConfigureModelSwitchesTheLiveTargetModel \
  TestServeReadsOnlyItsStateFolder \
  TestServeWithdrawsRunningBeforeStoreRelease \
  TestServerTeamDependenciesResolveOnOperation \
  TestSharedServerOwnershipRefinementResourcesAndLockReleaseAcrossRestarts \
  TestSharedServerSignalExitAndDetachedVerificationSurvival \
  TestStarterExitFailurePrecedesReady \
  TestTakenPortNamesHolderAndStep \
  TestTeamPreflightVersionAndPrograms \
  TestTeamProgramInstalledOffPath \
  TestTeamProgramStartup \
  TestUnusableOperatorFolderNamesStep \
  TestWorkspaceCaseAliasAdmission \
  TestWorkspaceGitAdmission \
  TestCheckModuleCacheFixtureCleanup

# Stored physical paths, private ownership/mode, start locks and roster folder identity.
run_selected ./internal/project \
  TestBrokenStateNamesPathAndStep \
  TestInitializeNamesClosedStateFolder \
  TestNewProjectAvoidsPortsOtherProjectsSaved \
  TestOpenServesOnlyTheNamedProjectState \
  TestRosterOmitsMissingWorkingFolders

# Repository/worktree path confinement and the owned command/output boundary.
run_selected ./internal/verify \
  TestCheckoutRecordsTheExactCommitInEitherObjectFormat \
  TestPrepareBuildDownloadsWhatTheModulesGoWorkNeeds \
  TestPrepareBuildFetchesModulesWhateverGitEnvironmentItInherits \
  TestProcessDiagnosticsStayPrivate \
  TestResolveInsideFollowsLinksAndStaysInside \
  TestRunChecksOutOnlyThePlansRepositoryWhateverGitEnvironmentItInherits \
  TestRunClonesARemoteRepositoryThroughTheUsersGitSettings \
  TestRunRefusesACommittedModuleLinkOutsideTheCheckout \
  TestWorkspaceBoundaryRootsAndEnclosingWorktreeRoot \
  TestWorkspaceBoundaryRootsPreservesCancellation

# Protected staging, directory identity/permissions and process-safe cleanup.
run_selected ./internal/scaffold \
  TestInitKeepsStagingWhenProcessesMayStillRun \
  TestInitLeavesNothingWhenAStepFails \
  TestInitNeverReplacesADirectoryCreatedMeanwhile \
  TestInitNeverReplacesAnEmptyDirectoryFileOrLink \
  TestInitPublishesTheBuiltTreeFromProtectedStorage \
  TestInitPublishesUnderAWriteSearchOnlyParent \
  TestInitRefusesAnExistingDirectory \
  TestInitRejectsCaseAliasedWorkspaceStaging \
  TestInitRejectsWorkspaceStaging \
  TestInitStopsAndCleansUpWhenCanceled \
  TestRunCommandOverridesBothTemporaryDirectories
