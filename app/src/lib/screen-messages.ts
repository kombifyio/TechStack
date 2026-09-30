// Customer-visible strings of screens, dialogs, wizard status and error copy.
// Merged into the main dictionary by i18n.ts (en, de) and locales/*.ts.
export const screenMessages = {
  en: {
    "state.active": "active",
    "state.adopting": "adopting",
    "state.archived": "archived",
    "state.awaiting_pairing": "awaiting pairing",
    "state.canceled": "canceled",
    "state.cancelled": "cancelled",
    "state.completed": "completed",
    "state.configured": "configured",
    "state.connected": "connected",
    "state.connecting": "connecting",
    "state.consistent": "consistent",
    "state.decommissioned": "decommissioned",
    "state.decommissioning": "decommissioning",
    "state.degraded": "degraded",
    "state.deploying": "deploying",
    "state.enrolling": "enrolling",
    "state.error": "error",
    "state.failed": "failed",
    "state.fresh": "fresh",
    "state.healthy": "healthy",
    "state.in_progress": "in progress",
    "state.managed": "managed",
    "state.migrating": "migrating",
    "state.missing": "missing",
    "state.not_reported": "not reported",
    "state.observed": "observed",
    "state.offline": "offline",
    "state.ok": "ok",
    "state.online": "online",
    "state.pending": "pending",
    "state.pending_verification": "pending verification",
    "state.planned": "planned",
    "state.present": "present",
    "state.provisioning": "provisioning",
    "state.reachable": "reachable",
    "state.ready": "ready",
    "state.recorded": "recorded",
    "state.revoked": "revoked",
    "state.rollout": "rollout",
    "state.running": "running",
    "state.stale": "stale",
    "state.stalled": "stalled",
    "state.starting": "starting",
    "state.stopped": "stopped",
    "state.succeeded": "succeeded",
    "state.success": "success",
    "state.unexpected": "unexpected",
    "state.unhealthy": "unhealthy",
    "state.unknown": "unknown",
    "ui.accordion.advancedSettings": "Advanced Settings",
    "ui.aiHandover.couldNotOpen": "kombify AI could not open: {error}",
    "ui.api.networkErrorCouldNotConnect":
      "Network error: Could not connect to Discovery API. Is the backend running?",
    "ui.api.scanPollingTimeout": "Scan polling timeout",
    "ui.api.serverSideAPIBaseURL":
      "Server-side API base URL is not configured. Set TECHSTACK_API_URL (runtime) or VITE_API_URL (build time).",
    "ui.argon2.addNumber": "Add at least one number.",
    "ui.argon2.addSymbols":
      "Add spaces or symbols to make the passphrase harder to guess.",
    "ui.argon2.longer":
      "Longer passphrases are easier to remember and stronger.",
    "ui.argon2.tooShort": "Passphrase too short (minimum {min} characters)",
    "ui.argon2.useAtLeast": "Use at least {min} characters.",
    "ui.auth.authInitFailed": "Auth init failed",
    "ui.auth.cloudAuthenticationNotConfigured":
      "Cloud authentication not configured",
    "ui.auth.invalidEmailOrPassword": "Invalid email or password",
    "ui.auth.portalSignInDidNot":
      "Portal sign-in did not establish a verified browser session",
    "ui.authCloud-link-complete.closeThisTabAndTry":
      "Close this tab and try again from the wizard.",
    "ui.authCloud-link-complete.kombifyCloudConnected":
      "kombify Cloud connected",
    "ui.authCloud-link-complete.kombifyCloudLinkKombifyTechstack":
      "kombify Cloud Link - kombify-Techstack",
    "ui.authCloud-link-complete.thisWindowClosesAutomatically":
      "This window closes automatically.",
    "ui.authCloud-link-complete.youCanCloseThisTab":
      "You can close this tab and return to the wizard.",
    "ui.authHandler.yourSessionHasExpiredPlease":
      "Your session has expired. Please sign in again.",
    "ui.authSso.noSsoTokenProvided": "No SSO token provided",
    "ui.authSso.ssoAuthenticationFailed": "SSO authentication failed",
    "ui.authSso.ssoAuthenticationKombifyTechstack":
      "SSO Authentication - kombify-Techstack",
    "ui.authSso.unableToVerifyYourCredentials":
      "Unable to verify your credentials",
    "ui.authSso.yourKombifyCloudSessionCould":
      "Your kombify Cloud session could not be restored. Please try again.",
    "ui.client.csrfTokenResponseMissingToken":
      "CSRF token response missing token",
    "ui.client.gatewayAuthenticationUnavailableSignIn":
      "Gateway authentication unavailable. Sign in again to verify your managed runtime entitlements.",
    "ui.client.networkError":
      "Network error: Could not connect to API. Is the backend reachable?",
    "ui.client.networkErrorAt":
      "Network error: Could not connect to API at {base}. Is the backend reachable?",
    "ui.client.parentGatewayTokenFailed": "parent gateway token failed",
    "ui.client.requestTimedOutAfterS": "Request timed out after {v1}s: {url}",
    "ui.client.serverReturnedHTMLInsteadOf":
      "{basePrefix}Server returned HTML instead of JSON ({status}). Is the backend reachable and are you authenticated?",
    "ui.clientLocal.checkingLocalAuthState": "Checking local auth state...",
    "ui.clientLocal.continueLocalSetup": "Continue local setup",
    "ui.clientLocal.createOrSignInAs":
      "Create or sign in as the local owner for this self-hosted Techstack. After setup, the operator UI opens directly.",
    "ui.clientLocal.createTheFirstLocalAdmin":
      "Create the first local admin for this device. After setup, this Windows client opens the operator UI directly.",
    "ui.clientLocal.creatingLocalOwner": "Creating local owner...",
    "ui.clientLocal.email": "Email",
    "ui.clientLocal.firstLocalAdmin": "First local admin",
    "ui.clientLocal.localOwnerSignIn": "Local owner sign-in",
    "ui.clientLocal.localOwnerSignedIn": "Local owner signed in.",
    "ui.clientLocal.localOwnerWasCreatedBut":
      "Local owner was created, but sign-in failed.",
    "ui.clientLocal.localSetupFailed": "Local setup failed.",
    "ui.clientLocal.localSignInFailed": "Local sign-in failed.",
    "ui.clientLocal.localTechstackSetup": "Local TechStack setup.",
    "ui.clientLocal.localTechstackSetupKombifyTechstack":
      "Local TechStack setup | kombify Techstack",
    "ui.clientLocal.ownerName": "Owner name: {name}",
    "ui.clientLocal.password": "Password",
    "ui.clientLocal.selfHostedLocalOwner": "Self-hosted local owner",
    "ui.clientLocal.signInLocally": "Sign in locally",
    "ui.clientLocal.signingIn": "Signing in...",
    "ui.clientLocal.thisDeviceIsAlreadyConfigured":
      "This device is already configured. Sign in with the local owner account to open Techstack.",
    "ui.clientOnboarding.connectAnExistingSelfHosted":
      "Connect an existing self-hosted server",
    "ui.clientOnboarding.connectServer": "Connect server",
    "ui.clientOnboarding.installLocally": "Install locally",
    "ui.clientOnboarding.localWindowsInstallation":
      "Local Windows installation",
    "ui.clientOnboarding.noTechstackAccountRequiredTechstack":
      "No Techstack account required. Techstack runs on this device and opens a token-protected enrollment channel for your private network.",
    "ui.clientOnboarding.setUpKombifyTechstackOn":
      "Set up kombify Techstack on this Windows device.",
    "ui.clientOnboarding.signInWithKombifyCloud": "Sign in with kombify Cloud",
    "ui.clientOnboarding.signInWithYourKombify":
      "Sign in with your kombify Cloud account and connect this desktop client.",
    "ui.clientOnboarding.startingLocally": "Starting locally...",
    "ui.clientOnboarding.thePrivateNetworkEnrollmentChannel":
      "The private-network enrollment channel could not be enabled.",
    "ui.clientOnboarding.thisClientIsTheLocal":
      "This client is the local desktop entry for orchestrating your own servers and StackKits. The default path installs Techstack locally on this device.",
    "ui.clientOnboarding.useLocally": "Use locally",
    "ui.clientOnboarding.useYourOwnSelfHosted":
      "Use your own self-hosted server instead",
    "ui.clientOnboarding.windowsClientOnboardingKombifyTechstack":
      "Windows Client Onboarding | kombify Techstack",
    "ui.cockpitSnapshot.monitoringCockpitCouldNotBe":
      "Monitoring cockpit could not be refreshed.",
    "ui.common.copiedCheck": "✓ Copied",
    "ui.common.copy": "Copy",
    "ui.common.sourceValue": "Source: {value}",
    "ui.common.unknown": "unknown",
    "ui.completeDashboard.allServices": "All services →",
    "ui.completeDashboard.nodesApps": "Nodes & apps",
    "ui.configFlow.apiUnreachable":
      "The API server is not reachable from your browser.",
    "ui.configFlow.apiUnreachableAt":
      "The API server at {base} is not reachable from your browser.",
    "ui.configFlow.capability": "Capability: {value}",
    "ui.configFlow.corsHint":
      "If the backend is running but the browser blocks the request (CORS), open DevTools → Console and look for a CORS error.",
    "ui.configFlow.createFailedAt": "Create failed at: {phase}",
    "ui.configFlow.deploymentNameExists":
      'A StackKit deployment named "{name}" already exists in your Homelab. Open the existing deployment or choose a different name.',
    "ui.configFlow.errorCode": "Error code: {value}",
    "ui.configFlow.missingFeatures": "Missing features: {value}",
    "ui.configFlow.nextStep": "Next step: {value}",
    "ui.configFlow.nextSteps": "Next steps:",
    "ui.configFlow.pleaseFix": "Please fix the following:",
    "ui.configFlow.provider": "Provider: {value}",
    "ui.configFlow.reason": "Reason: {value}",
    "ui.configFlow.requestId": "Request ID: {value}",
    "ui.configFlow.requiredFeatures": "Required features: {value}",
    "ui.configFlow.serverEncountered":
      "The server encountered an error ({status}). {message}",
    "ui.configFlow.stackId": "Stack ID: {value}",
    "ui.configFlow.stackkitsValidation": "StackKits validation:",
    "ui.configFlow.step": "Step: {value}",
    "ui.configFlow.technicalDetails": "Technical details: {value}",
    "ui.configurationFlow.aPreviousSubmissionAlreadyCompleted":
      "A previous submission already completed",
    "ui.configurationFlow.anEarlierSubmissionFromThis":
      "An earlier submission from this browser session already completed with different answers. A fresh attempt key was generated — submit again, or open the existing deployment.",
    "ui.configurationFlow.anUnexpectedErrorOccurredPlease":
      "An unexpected error occurred. Please check the browser console for details.",
    "ui.configurationFlow.authenticationRequired": "Authentication required",
    "ui.configurationFlow.cannotConnectToBackend": "Cannot connect to backend",
    "ui.configurationFlow.deploymentFailed": "Deployment failed",
    "ui.configurationFlow.managedServerIsNotActive":
      "Managed server is not active",
    "ui.configurationFlow.permissionDenied": "Permission denied",
    "ui.configurationFlow.retryingIsUnlikelyToHelp":
      "Retrying is unlikely to help until the server-side issue is fixed.",
    "ui.configurationFlow.serverError": "Server error",
    "ui.configurationFlow.stackkitDeploymentNameAlreadyExists":
      "StackKit deployment name already exists",
    "ui.configurationFlow.theBackendRejectedThisStackkit":
      "The backend rejected this StackKit deployment request with a conflict.",
    "ui.configurationFlow.useTheAdminPasswordConfigured":
      "(use the admin password configured for this instance)",
    "ui.configurationFlow.validatingConfiguration":
      "Validating configuration...",
    "ui.configurationFlow.validatingWithStackkits":
      "Validating with StackKits...",
    "ui.configurationFlow.youCanRetryIfThis":
      "You can retry. If this happens again, share the error code and request ID with support.",
    "ui.configurationFlow.youDonTHavePermission":
      "You don't have permission to deploy StackKits, and the server did not provide a specific reason. Please share this with support.",
    "ui.configurationFlow.youMustBeLoggedIn":
      "You must be logged in to deploy a StackKit.",
    "ui.configurationFlow.youMustBeLoggedIn2":
      "You must be logged in to deploy a StackKit. Please log in first.",
    "ui.connectDevices.techstackClient": "Techstack client",
    "ui.creationCompletion.ownerSeedFor":
      "The owner seed is prepared for {email}.",
    "ui.creationController.theAdditionalNode": "The additional Node",
    "ui.creationFailure.lease": "Lease",
    "ui.creationInstall.previewExpires": "Preview expires at {time}",
    "ui.creationLease.sshHost":
      "kombify will use the captured SSH connection details for the existing server at {host}.",
    "ui.creationLease.sshHostUser":
      "kombify will use the captured SSH connection details for the existing server at {host} as {user}.",
    "ui.creationLease.sshUser":
      "kombify will use the captured SSH connection details for the existing server as {user}.",
    "ui.creationRequirements.minCpu": "CPU: Min. {count} cores",
    "ui.creationRequirements.minRam": "RAM: Min. {size}GB",
    "ui.creationRequirements.minVersion": "min: {version}",
    "ui.creationRun.notConnectedYet": "Not connected yet",
    "ui.creationRun.notReachableYet": "Not reachable yet",
    "ui.creationRun.pairingTokenExpires": "Pairing token expires at {slot}.",
    "ui.creationRun.resumeStartingAt":
      "Starting at {time}, the server can authorize a safe resume on the same VM.",
    "ui.creationRun.stepOf": "Step {step} of {total}",
    "ui.credentialForm.accessEntriesNeedAtLeast":
      "Access entries need at least a user, URL, or secret",
    "ui.credentialForm.administrativeToolsAndOperationalSurfaces":
      "Administrative tools and operational surfaces.",
    "ui.credentialForm.apiKey": "API Key",
    "ui.credentialForm.apiLabelOwner": "API Label / Owner",
    "ui.credentialForm.bearerTokenOrRefreshToken":
      "Bearer token or refresh token...",
    "ui.credentialForm.breakGlassSecretsAndReveal":
      "Break-glass secrets and reveal-only recovery material.",
    "ui.credentialForm.cert": "Cert",
    "ui.credentialForm.certificate": "Certificate",
    "ui.credentialForm.certificateContent": "Certificate Content",
    "ui.credentialForm.credentialType": "Credential Type",
    "ui.credentialForm.describeTheRecoveryPathStorage":
      "Describe the recovery path, storage location, or operator instructions...",
    "ui.credentialForm.describeTheUserGateOr":
      "Describe the user, gate, or access policy this entry belongs to...",
    "ui.credentialForm.eGBreakGlassEnvelope": "e.g., Break-Glass Envelope",
    "ui.credentialForm.eGPocketbaseAdmin": "e.g., PocketBase Admin",
    "ui.credentialForm.eGTailscaleDeviceApproval":
      "e.g., Tailscale Device Approval",
    "ui.credentialForm.enterPassword": "Enter password...",
    "ui.credentialForm.enterSecretValue": "Enter secret value...",
    "ui.credentialForm.expiryDate": "Expiry Date",
    "ui.credentialForm.failedToSaveCredential": "Failed to save credential",
    "ui.credentialForm.keyIdentifierLabel": "Key Identifier / Label",
    "ui.credentialForm.name": "Name",
    "ui.credentialForm.nameIsRequired": "Name is required",
    "ui.credentialForm.notes": "Notes",
    "ui.credentialForm.oauthToken": "OAuth Token",
    "ui.credentialForm.operatorAccount": "Operator / Account",
    "ui.credentialForm.optional": "(optional)",
    "ui.credentialForm.otpauthTotpOrBase32Secret":
      "otpauth://totp/... or base32 secret",
    "ui.credentialForm.portalPolicyUrl": "Portal / Policy URL",
    "ui.credentialForm.recoveryUrlEndpoint": "Recovery URL / Endpoint",
    "ui.credentialForm.saving": "Saving...",
    "ui.credentialForm.sshKey": "SSH Key",
    "ui.credentialForm.token": "Token",
    "ui.credentialForm.toolUrl": "Tool URL",
    "ui.credentialForm.toolUrlIsRequired": "Tool URL is required",
    "ui.credentialForm.userIdentity": "User / Identity",
    "ui.credentialForm.usernameEmail": "Username / Email",
    "ui.credentialForm.usersPortalsIpDeviceGating":
      "Users, portals, IP/device gating, and access controls.",
    "ui.credentialForm.walletArea": "Wallet Area",
    "ui.credentialForm.whatThisToolIsFor":
      "What this tool is for, who uses it, and any launch context...",
    "ui.crypto.decryptionFailedWrongPassword":
      "Decryption failed. Wrong password?",
    "ui.custody.leasesWithoutNode.one": "{count} lease without a Node",
    "ui.custody.leasesWithoutNode.other": "{count} leases without a Node",
    "ui.custody.wasAddress": "was {ip}",
    "ui.custodyLeasesPanel.decommissioning": "Decommissioning...",
    "ui.custodyLeasesPanel.leaseArchived": "Lease archived",
    "ui.custodyLeasesPanel.leaseCancelled": "Lease cancelled",
    "ui.custodyLeasesPanel.neverObserved": "Never observed",
    "ui.custodyLeasesPanel.noExecutionAuthorityLegacyOr":
      "No execution authority (legacy or unbound lease)",
    "ui.custodyLeasesPanel.nodeNeverFinishedEnrolling":
      "Node never finished enrolling",
    "ui.custodyLeasesPanel.resolving": "Resolving...",
    "ui.custodyLeasesPanel.vmNoLongerExistsAt":
      "VM no longer exists at the provider",
    "ui.dashboardServiceSheet.noHealthReported": "No health reported",
    "ui.discovery.addCredentials.one": "Add {count} Credential",
    "ui.discovery.addCredentials.other": "Add {count} Credentials",
    "ui.discovery.selected": "{count} selected",
    "ui.easyWizard.pleaseSelectAnAccessMode":
      "Please select an access mode (Home only or Anywhere)",
    "ui.easyWizard.pleaseSelectWhoWillUse":
      "Please select who will use your server",
    "ui.easyWizard.serverHostOrIpIs":
      "Server host or IP is required for direct connection",
    "ui.errors.anUnknownErrorOccurred": "An unknown error occurred",
    "ui.export.decryptionNotAvailablePleaseUse":
      "Decryption not available. Please use a modern browser with HTTPS.",
    "ui.export.encryptionNotAvailablePleaseUse":
      "Encryption not available. Please use a modern browser with HTTPS.",
    "ui.export.failedToParseExportFile": "Failed to parse export file",
    "ui.export.invalidCredentialMissingNameOr":
      "Invalid credential: missing name or kind",
    "ui.export.invalidExportFileMalformedJSON":
      "Invalid export file: malformed JSON",
    "ui.export.invalidExportFileStructure": "Invalid export file structure",
    "ui.export.invalidExportFormatExpectedArray":
      "Invalid export format: expected array",
    "ui.export.passwordRequiredForEncryptedExport":
      "Password required for encrypted export",
    "ui.export.unsupportedExportVersionExpected":
      "Unsupported export version: {version}. Expected: {EXPORT_VERSION}",
    "ui.featureGate.enableThisFeatureInSettings":
      "Enable this feature in Settings → Features",
    "ui.featureGate.grantConsentInSettingsFeatures":
      "Grant consent in Settings → Features to enable",
    "ui.featureGate.thisFeatureRequiresAdminPrivileges":
      "This feature requires admin privileges",
    "ui.features.failedToLoadFeatures": "Failed to load features",
    "ui.footer.copyright": "© {year} Kombiverse Labs",
    "ui.footer.copyrightTagline":
      "© {year} Kombiverse Labs. Made for humans, powered by intelligence.",
    "ui.footer.forHomelabCommunity": "for the homelab community",
    "ui.footer.instanceShort": "instance:{id}",
    "ui.footer.instanceTitle": "Instance {id}",
    "ui.footer.toggleLogoStyle": "Toggle logo style ({style})",
    "ui.footerModern.about": "About",
    "ui.footerModern.contact": "Contact",
    "ui.footerModern.docs": "Docs",
    "ui.footerModern.features": "Features",
    "ui.footerModern.madeWith": "Made with",
    "ui.footerModern.privacy": "Privacy",
    "ui.footerModern.terms": "Terms",
    "ui.groupedTaskList.done": "Done",
    "ui.homelab.agentVersions.one": "Reported agent version: {versions}.",
    "ui.homelab.agentVersions.other": "Reported agent versions: {versions}.",
    "ui.homelab.connectedOfTotal": "{connected}/{total} connected",
    "ui.homelab.latestFailed": "Latest {type} failed — {detail}",
    "ui.homelab.noSourceObserved":
      "Services come from two sources: a completed StackKit rollout, and the containers and units the Agent discovers on the host. Neither source has been observed yet: no StackKit manifest was found and the agent ran no service discovery.",
    "ui.homelab.onlineCount": "{count} online",
    "ui.homelab.operationsNotLoaded.one":
      "{count} StackKit deployment operation could not be loaded.",
    "ui.homelab.operationsNotLoaded.other":
      "{count} StackKit deployment operations could not be loaded.",
    "ui.homelab.partialRolloutBadge": "partial rollout",
    "ui.homelab.phase": "Phase: {phase}",
    "ui.homelab.preparationFailed":
      "StackKit preparation failed on connected Node — {detail}",
    "ui.homelab.probeAnswered":
      "The runtime answered the probe and the Guard heartbeat is current ({state}).",
    "ui.homelab.probeOffline":
      'The provider reports the machine as "{machineState}" and enrollment as "{enrollment}". Reconnect can only re-run that probe — it cannot restart the kombify Agent on the machine. The Node stays offline until the Agent sends a heartbeat again, so continue in the Node details, where the enrollment command and the last contact are shown.',
    "ui.homelab.recorded": "{count} recorded",
    "ui.homelab.retryFailed": "Rollout could not be retried: {message}",
    "ui.homelab.rolloutFailed": "Rollout failed: {message}",
    "ui.homelab.sshFailed": "SSH connection to your Node failed — {detail}",
    "ui.homelabDashboardPage.0VerifiedConnectedOperationsEvidence":
      "0 verified connected · operations evidence unavailable",
    "ui.homelabDashboardPage.agentDiscoveryReportedNoRunning":
      "Agent discovery reported no running containers or units. No StackKit manifest has been observed, so declared StackKit services are still unknown.",
    "ui.homelabDashboardPage.bothServiceSourcesReportedAn":
      "Both service sources reported an empty inventory: the StackKit manifest contained no services, and agent discovery found no running containers or units.",
    "ui.homelabDashboardPage.confirmThatTheProviderResource":
      "Confirm that the provider resource has already been removed. Techstack will archive only its stale custody record and will not delete a provider resource.",
    "ui.homelabDashboardPage.custodyRecordUnchangedTheTechstack":
      "Custody record unchanged: the Techstack gateway could not reach the backend. Retry this exact record when the service is available.",
    "ui.homelabDashboardPage.custodyResolutionFailed":
      "Custody resolution failed",
    "ui.homelabDashboardPage.decommissionFailed": "Decommission failed",
    "ui.homelabDashboardPage.deployStackkit": "Deploy StackKit",
    "ui.homelabDashboardPage.lastVerifiedStateRetainedThe":
      "Last verified state retained — the refresh failed and retries automatically.",
    "ui.homelabDashboardPage.managedRuntime": "Managed runtime",
    "ui.homelabDashboardPage.managedRuntimeLeaseAllocationMetadata":
      "Managed-runtime lease/allocation metadata exists, but the current provider and Guard state could not be verified.",
    "ui.homelabDashboardPage.noNodeHasReportedAn":
      "No Node has reported an inventory yet.",
    "ui.homelabDashboardPage.nodeIsReportingAgain": "Node is reporting again",
    "ui.homelabDashboardPage.openTheNodeDetailsTo":
      "Open the Node details to check the Agent and the last rollout.",
    "ui.homelabDashboardPage.operationsDataIsNotAvailable":
      "Operations data is not available for this homelab yet.",
    "ui.homelabDashboardPage.operationsDataIsNotAvailable2":
      "Operations data is not available yet",
    "ui.homelabDashboardPage.reconnect": "Reconnect",
    "ui.homelabDashboardPage.reconnectFailed": "Reconnect failed",
    "ui.homelabDashboardPage.reconnecting": "Reconnecting...",
    "ui.homelabDashboardPage.refresh": "Refresh",
    "ui.homelabDashboardPage.resolveExactRecord": "Resolve exact record",
    "ui.homelabDashboardPage.resolveRecord": "Resolve record",
    "ui.homelabDashboardPage.resolveStaleCustodyRecord":
      "Resolve stale custody record?",
    "ui.homelabDashboardPage.retry": "Retry",
    "ui.homelabDashboardPage.retryExactCleanup": "Retry exact cleanup",
    "ui.homelabDashboardPage.review": "Review",
    "ui.homelabDashboardPage.reviewStart": "Review + Start",
    "ui.homelabDashboardPage.rolloutReadinessIsUnavailableRefresh":
      "Rollout readiness is unavailable. Refresh the operations data before starting.",
    "ui.homelabDashboardPage.runtimeAnsweredButTheNode":
      "Runtime answered, but the Node is still offline",
    "ui.homelabDashboardPage.stackkitDeployment": "StackKit deployment",
    "ui.homelabDashboardPage.starting": "Starting...",
    "ui.homelabDashboardPage.theAgentVersionWasNot":
      "The agent version was not reported.",
    "ui.homelabDashboardPage.theFailedCleanupIsNot":
      "The failed cleanup is not bound to an actionable lease. Refresh the lifecycle evidence and use the action on the exact custody record.",
    "ui.homelabDashboardPage.theFailedStackkitDeploymentIs":
      "The failed StackKit deployment is no longer part of this homelab. Refresh the homelab and retry the exact deployment.",
    "ui.homelabDashboardPage.thePersistedRolloutIsWaiting":
      "The persisted rollout is waiting for its next checkpoint.",
    "ui.homelabDashboardPage.theRuntimeProbeReturnedNo":
      "The runtime probe returned no reason. Retry, or open the Node details for the full runtime record.",
    "ui.homelabDashboardPage.theStackkitManifestSourceReported":
      "The StackKit manifest source reported no services. Service discovery has not been observed, so containers and units are still unknown.",
    "ui.homelabDashboardPage.thisFailedRunCannotBe":
      "This failed run cannot be retried automatically and safely.",
    "ui.homelabDashboardPage.you": "You",
    "ui.homelabDashboardPage.yourHomelab": "Your homelab",
    "ui.homelabModel.lifecycleConnectionHealth":
      "Lifecycle {lifecycle} · Connection {connection} · Health {health}",
    "ui.homelabModel.managedVPS": "Managed VPS",
    "ui.homelabModel.ownDevice": "Own device",
    "ui.hypervisor.diskGiB": "{label} (GiB)",
    "ui.hypervisor.storageAvail": "{name} · {size} GiB",
    "ui.hypervisorSelection.homeAssistantOs": "Home Assistant OS",
    "ui.hypervisorSelection.ramMib": "RAM (MiB)",
    "ui.identity.alien": "Alien",
    "ui.identity.astronaut": "Astronaut",
    "ui.identity.circuit": "Circuit",
    "ui.identity.cybershield": "Cybershield",
    "ui.identity.dragon": "Dragon",
    "ui.identity.ghost": "Ghost",
    "ui.identity.hologram": "Hologram",
    "ui.identity.myHomelab": "My Homelab",
    "ui.identity.nebula": "Nebula",
    "ui.identity.ninja": "Ninja",
    "ui.identity.phoenix": "Phoenix",
    "ui.identity.quantum": "Quantum",
    "ui.identity.robot": "Robot",
    "ui.identity.rocket": "Rocket",
    "ui.identity.satellite": "Satellite",
    "ui.identity.unicorn": "Unicorn",
    "ui.identity.wizard": "Wizard",
    "ui.importExport.downloadEncrypted.one":
      "Download {count} credential as encrypted JSON",
    "ui.importExport.downloadEncrypted.other":
      "Download {count} credentials as encrypted JSON",
    "ui.importExport.exportedSummary": "Exported: {date} • {count} items",
    "ui.importExport.notEncrypted":
      "{format} exports are not encrypted. Store the file securely.",
    "ui.importExport.readyToImport.one": "Ready to import {count} credential",
    "ui.importExport.readyToImport.other":
      "Ready to import {count} credentials",
    "ui.importExport.willBeExported.one":
      "{count} credential will be exported.",
    "ui.importExport.willBeExported.other":
      "{count} credentials will be exported.",
    "ui.importExportModal.back": "Back",
    "ui.importExportModal.bitwardenExportFailed": "Bitwarden export failed",
    "ui.importExportModal.bitwardenJson": "Bitwarden JSON",
    "ui.importExportModal.cancel": "Cancel",
    "ui.importExportModal.chooseDifferentFile": "Choose Different File",
    "ui.importExportModal.clickToSelectFile": "Click to select file",
    "ui.importExportModal.compatibleWithKeepassLastpassAnd":
      "Compatible with KeePass, LastPass, and generic password managers.",
    "ui.importExportModal.confirmPassword": "Confirm Password",
    "ui.importExportModal.csvExportFailed": "CSV export failed",
    "ui.importExportModal.decrypt": "Decrypt",
    "ui.importExportModal.decryptionFailed": "Decryption failed",
    "ui.importExportModal.decryptionPassword": "Decryption Password",
    "ui.importExportModal.download": "Download",
    "ui.importExportModal.encryptExport": "Encrypt export",
    "ui.importExportModal.encryptionNotAvailableRequiresHttps":
      "Encryption not available (requires HTTPS)",
    "ui.importExportModal.encryptionPassword": "Encryption Password",
    "ui.importExportModal.enterExportPassword": "Enter export password",
    "ui.importExportModal.exportCredentials": "Export Credentials",
    "ui.importExportModal.exportFailed": "Export failed",
    "ui.importExportModal.exportFormat": "Export Format",
    "ui.importExportModal.exportWallet": "Export Wallet",
    "ui.importExportModal.exporting": "Exporting...",
    "ui.importExportModal.exportingWithoutEncryptionWillSave":
      "Exporting without encryption will save all secrets in plain text. Only use this for testing.",
    "ui.importExportModal.failedToReadFile": "Failed to read file",
    "ui.importExportModal.import": "Import",
    "ui.importExportModal.importCredentials": "Import Credentials",
    "ui.importExportModal.importDirectlyIntoBitwardenOr":
      "Import directly into Bitwarden or compatible vaults.",
    "ui.importExportModal.importExportCredentials":
      "Import / Export Credentials",
    "ui.importExportModal.importFailed": "Import failed",
    "ui.importExportModal.importWallet": "Import Wallet",
    "ui.importExportModal.important": "Important:",
    "ui.importExportModal.importedCredentialsWillBeAdded":
      "Imported credentials will be added as new entries. Duplicates are not automatically merged.",
    "ui.importExportModal.importing": "Importing...",
    "ui.importExportModal.keepThisPasswordSafeWithout":
      "Keep this password safe! Without it, you won't be able to restore your credentials.",
    "ui.importExportModal.kombifyTechstackJson": "kombify-Techstack JSON",
    "ui.importExportModal.kombifyTechstackWalletExportJson":
      "kombify-Techstack wallet export (.json)",
    "ui.importExportModal.min8Characters": "Min. 8 characters",
    "ui.importExportModal.nativeFormatWithEncryptionSupport":
      "Native format with encryption support. Best for backup and restore.",
    "ui.importExportModal.note": "Note:",
    "ui.importExportModal.passwordIsRequired": "Password is required",
    "ui.importExportModal.passwordIsRequiredForEncrypted":
      "Password is required for encrypted export",
    "ui.importExportModal.passwordMustBeAtLeast":
      "Password must be at least 8 characters",
    "ui.importExportModal.passwordsDoNotMatch": "Passwords do not match",
    "ui.importExportModal.reEnterPassword": "Re-enter password",
    "ui.importExportModal.restoreCredentialsFromABackup":
      "Restore credentials from a backup file",
    "ui.importExportModal.thisExportIsEncryptedEnter":
      "This export is encrypted. Enter the password to decrypt.",
    "ui.importExportModal.universalCsv": "Universal CSV",
    "ui.importExportModal.warning": "Warning:",
    "ui.inAppDialog.confirm": "Confirm",
    "ui.inAppDialog.continue": "Continue",
    "ui.integration.admin": "Admin",
    "ui.integration.grafanaDashboardCredentials":
      "Grafana dashboard credentials",
    "ui.integration.headscaleAPIKeyForManagement":
      "Headscale API key for management",
    "ui.integration.pocketbaseAdminDashboardCredentials":
      "PocketBase admin dashboard credentials",
    "ui.integration.traefikDashboardBasicAuth": "Traefik dashboard basic auth",
    "ui.inventory.nodeCount.one": "{count} Node",
    "ui.inventory.nodeCount.other": "{count} Nodes",
    "ui.inventory.serviceCount.one": "{count} service",
    "ui.inventory.serviceCount.other": "{count} services",
    "ui.inventory.telemetryNodes.one": "{count} telemetry Node",
    "ui.inventory.telemetryNodes.other": "{count} telemetry Nodes",
    "ui.localOwner.localOwnerCreationIsOnly":
      "Local owner creation is only available during first-run setup.",
    "ui.login.bootstrapPassword": "Bootstrap password:",
    "ui.login.breakGlassAdminIsNot":
      "Break-glass admin is not initialized yet.",
    "ui.login.cloudSignInIsNot": "Cloud sign-in is not configured yet.",
    "ui.login.continueAsLocalOwner": "Continue as local owner",
    "ui.login.continueWithKombifyCloud": "Continue with kombify Cloud",
    "ui.login.couldNotContactBackend": "Could not contact backend.",
    "ui.login.couldNotContactBackendWith":
      "Could not contact backend: {message}",
    "ui.login.emergencyAdmin": "Emergency admin",
    "ui.login.emergencyAdminIsNotInitialized":
      "Emergency admin is not initialized yet.",
    "ui.login.emergencyEmail": "Emergency email:",
    "ui.login.emergencyLoginFailed": "Emergency login failed.",
    "ui.login.emergencyPassword": "Emergency password",
    "ui.login.emergencyRecoveryOnlyDayTo":
      "Emergency recovery only. Day-to-day sign-in stays on kombify Cloud or the local owner path.",
    "ui.login.kombifyCloudSignInIs":
      "kombify Cloud sign-in is not available right now.",
    "ui.login.loading": "Loading...",
    "ui.login.openKombifyCloudInBrowser": "Open kombify Cloud in browser",
    "ui.login.redirectingToKombifyCloud": "Redirecting to kombify Cloud...",
    "ui.login.retryConnection": "Retry connection",
    "ui.login.revealBootstrapPassword": "Reveal bootstrap password",
    "ui.login.revealFailed": "Reveal failed.",
    "ui.login.revealing": "Revealing...",
    "ui.login.signInAsEmergencyAdmin": "Sign in as emergency admin",
    "ui.login.signInKombifyTechstack": "Sign in | kombify Techstack",
    "ui.login.signInWithKombifyCloud": "Sign in with kombify Cloud.",
    "ui.login.signInWithTheLocal":
      "Sign in with the local owner account for this Techstack.",
    "ui.login.techstackReconnectsThroughTheKombify":
      "Techstack reconnects through the kombify Cloud page that contains this view. It will not open a second sign-in page inside this frame.",
    "ui.login.theBootstrapPasswordHasExpired":
      "The bootstrap password has expired. Restart the server to generate a new one.",
    "ui.login.tooManyLoginAttemptsPlease":
      "Too many login attempts. Please wait and try again.",
    "ui.login.tryAgain": "Try again",
    "ui.login.useTheStoredEmergencyPassword":
      "Use the stored emergency password",
    "ui.login.youHaveBeenSignedOut": "You have been signed out.",
    "ui.loginExperience.kombifyCloudSignInCompleted":
      "kombify Cloud sign-in completed, but Techstack could not create a browser session. Try again or contact support.",
    "ui.managed.sizeGB": "{size} GB",
    "ui.managed.sizeGiB": "{size} GiB",
    "ui.managedCreationFlow.addNode": "Add Node",
    "ui.managedCreationFlow.failedToLoadNodeInventory":
      "Failed to load Node inventory.",
    "ui.managedCreationFlow.failedToPrepareNodeRegistration":
      "Failed to prepare Node registration.",
    "ui.managedCreationFlow.newStackkitMainNode": "New StackKit / main Node",
    "ui.managedCreationFlow.preparingNode": "Preparing Node...",
    "ui.managedCreationFlow.proxmoxHypervisor": "Proxmox hypervisor",
    "ui.managedCreationFlow.selectCentronOrIonosExplicitly":
      "Select Centron or IONOS explicitly before adding a managed Node to this historical StackKit deployment.",
    "ui.managedCreationFlow.stackkitDeploymentNotFound":
      "StackKit deployment not found.",
    "ui.managedCreationFlow.workerOrStorageForThis":
      "Worker or storage for this StackKit",
    "ui.managedRuntimeRecreatePanel.thisProvisionsAndBillsA":
      "This provisions and bills a new managed server generation. Techstack will re-establish enrollment, endpoints, Guard evidence, and the StackKit rollout through the normal creation flow.",
    "ui.monitoring.acrossEpisodes.one": "across {count} episode",
    "ui.monitoring.acrossEpisodes.other": "across {count} episodes",
    "ui.monitoring.availabilityHistoryIsUnavailableOn":
      "Availability history is unavailable on this deployment.",
    "ui.monitoring.availabilityWindow": "Availability · {window}",
    "ui.monitoring.dailyState": "Daily state",
    "ui.monitoring.dailyStateLabel": "{name}: daily connection state",
    "ui.monitoring.downFor": "{duration} down",
    "ui.monitoring.downtimeByCause": "Downtime by cause",
    "ui.monitoring.downtimeEpisodes": "Downtime episodes",
    "ui.monitoring.isAnythingOnFire": "is anything on fire",
    "ui.monitoring.lastVerifiedCountRetainedThe":
      "Last verified count retained — the inventory refresh failed",
    "ui.monitoring.lifecycleConnectionAndHealthAre":
      "Lifecycle, connection and health are read together and never collapsed",
    "ui.monitoring.meanTimeToRecover": "Mean time to recover",
    "ui.monitoring.monitoring": "Monitoring",
    "ui.monitoring.monitoringDataCouldNotBe":
      "Monitoring data could not be loaded.",
    "ui.monitoring.noData": "no data",
    "ui.monitoring.noDowntimeInThisWindow": "No downtime in this window.",
    "ui.monitoring.noNodesHaveReportedYet":
      "No nodes have reported yet. Enroll a node and its state appears here.",
    "ui.monitoring.noRecordedDowntimeInThis":
      "No recorded downtime in this window.",
    "ui.monitoring.noServersInThisWindow": "No servers in this window.",
    "ui.monitoring.notReporting": "{count} not reporting",
    "ui.monitoring.notYetRecovered": "· not yet recovered",
    "ui.monitoring.ongoing": "ongoing",
    "ui.monitoring.openHistory": "Open history",
    "ui.monitoring.overObserved": "over {duration} observed",
    "ui.monitoring.recoveredBy": "· recovered by",
    "ui.monitoring.recoveredOngoingExcluded":
      "{count} recovered · ongoing excluded",
    "ui.monitoring.refreshing": "Refreshing...",
    "ui.monitoring.remove": "Remove",
    "ui.monitoring.removeFrom": "Remove {name} from Monitoring?",
    "ui.monitoring.removeFromAria": "Remove {name} from Monitoring",
    "ui.monitoring.removing": "Removing...",
    "ui.monitoring.rightNow": "Right now",
    "ui.monitoring.selectNodeHint":
      "select a node for its metrics and its own history",
    "ui.monitoring.serverCouldNotBeRemoved": "Server could not be removed.",
    "ui.monitoring.staleCountsAsDowntime":
      "stale counts as downtime — service cannot be confirmed",
    "ui.monitoring.theCanonicalServerInventoryIs":
      "The canonical server inventory is unavailable.",
    "ui.monitoring.thisRevokesTheBoundGuard":
      "This revokes the bound Guard Agent and hides the node from the current fleet. The physical server and its provider account stay untouched.",
    "ui.monitoring.totalDowntime": "Total downtime",
    "ui.monitoringServer.alertsInScope": "{count} in scope for this node",
    "ui.monitoringServer.allEpisodes": "All episodes →",
    "ui.monitoringServer.dailyState30":
      "{name}: daily connection state over 30 days",
    "ui.monitoringServer.firingSince": "firing since {time}",
    "ui.monitoringServer.inactive": "inactive",
    "ui.monitoringServer.inventoryRevision": "inventory revision {revision}",
    "ui.monitoringServer.pageTitle": "{name} — Monitoring",
    "ui.monitoringServer.panelLast24h": "{title} over the last 24 hours",
    "ui.monitoringServer.peak": "peak {value}",
    "ui.monitoringServer.rangeQueryHint":
      'every panel is one range query scoped to node_id="{id}"',
    "ui.monitoringServer.server": "Server",
    "ui.monitoringServer.unrecorded": "unrecorded",
    "ui.monitoringServerId.alertRules": "Alert rules",
    "ui.monitoringServerId.availability30D": "Availability · 30 d",
    "ui.monitoringServerId.containersRunning": "Containers running",
    "ui.monitoringServerId.cpuUtilisation": "CPU utilisation",
    "ui.monitoringServerId.diskUsed": "Disk used",
    "ui.monitoringServerId.guardIsRevokedImmediatelyThe":
      "Guard is revoked immediately. The physical server stays in place.",
    "ui.monitoringServerId.memoryUsed": "Memory used",
    "ui.monitoringServerId.metricsLast24H": "Metrics · last 24 h",
    "ui.monitoringServerId.noAlertRulesApplyHere": "No alert rules apply here.",
    "ui.monitoringServerId.noHistoryForThisNode":
      "No history for this node yet.",
    "ui.monitoringServerId.noMetricsBackendAnsweredFor":
      "No metrics backend answered for this node. Availability below is derived from the transition timeline and does not depend on it.",
    "ui.monitoringServerId.refreshing": "Refreshing…",
    "ui.monitoringServerId.removeFromView": "Remove from view",
    "ui.monitoringServerId.thisNodeSHistory": "This node’s history",
    "ui.monitoringServerId.thisServerCouldNotBe":
      "This server could not be loaded.",
    "ui.navItems.noServicesRegisteredYet": "No services registered yet.",
    "ui.nodeActions.assignToThisStackkitDeployment":
      "Assign to this StackKit deployment",
    "ui.nodeActions.assigning": "Assigning...",
    "ui.nodeActions.canonicalManagedAccessIsNot":
      "Canonical managed access is not available yet.",
    "ui.nodeActions.connection": "Node connection: {state}",
    "ui.nodeRow.hideSystemServices": "Hide system services",
    "ui.nodeRow.showFewerApps": "Show fewer apps",
    "ui.nodeRow.systemServices.one": "{count} system service",
    "ui.nodeRow.systemServices.other": "{count} system services",
    "ui.ownerState.connectYourKombifyCloudProfile":
      "Connect your kombify Cloud profile (with a verified email) to use it as the owner",
    "ui.ownerState.couldNotHashTheRecovery":
      "Could not hash the recovery passphrase.",
    "ui.ownerState.couldNotStartTheKombify":
      "Could not start the kombify Cloud link.",
    "ui.ownerState.emailIsRequiredForPasswordless":
      "Email is required for passwordless authentication",
    "ui.ownerState.finishTheConnectionInThe":
      "Finish the connection in the browser where you started it. Start the connection again.",
    "ui.ownerState.kombifyCloudLoginWasCancelled":
      "kombify Cloud login was cancelled or failed.",
    "ui.ownerState.linkingTheKombifyCloudProfile":
      "Linking the kombify Cloud profile failed. Try again.",
    "ui.ownerState.recoveryPassphraseMustBeAt":
      "Recovery passphrase must be at least {MIN_RECOVERY_PASSPHRASE_LENGTH} characters",
    "ui.ownerState.recoveryPassphrasesDoNotMatch":
      "Recovery passphrases do not match",
    "ui.ownerState.theKombifyCloudEmailIs":
      "The kombify Cloud email is not verified. Verify it in your Cloud account, then link again.",
    "ui.ownerState.theKombifyCloudProfileHas":
      "The kombify Cloud profile has no email address.",
    "ui.ownerState.theLinkRequestExpiredStart":
      "The link request expired. Start the connection again.",
    "ui.ownerState.theSelectedOwnerSourceIs":
      "The selected owner source is no longer supported. Choose a local owner or link your kombify Cloud profile.",
    "ui.ownerState.thisInstanceHasNoKombify":
      "This instance has no kombify Cloud login configured.",
    "ui.pairing.nodeConnection": "Node connection",
    "ui.pairing.theConnectionCommandCouldNot":
      "The connection command could not be prepared. Try again.",
    "ui.pairing.theOriginalNodeConfigurationIs":
      "The original Node configuration is unavailable. Return to Add Node to prepare the connection.",
    "ui.people.activationExpired": "Activation expired",
    "ui.people.activationPending": "Activation pending",
    "ui.people.appCount.one": "{count} app",
    "ui.people.appCount.other": "{count} apps",
    "ui.people.connectedClientsAppearHereOnce":
      "Connected clients appear here once kombify Connect device management is available.",
    "ui.people.deviceListUnavailable": "Device list unavailable.",
    "ui.people.homelabOwner": "Homelab owner",
    "ui.people.householdListUnavailableTheHomelab":
      "Household list unavailable: the homelab identity provider did not answer.",
    "ui.people.identityProviderNotReady": "Identity provider not ready",
    "ui.people.identityStatusUnavailable": "Identity status unavailable",
    "ui.people.member": "Member",
    "ui.people.notInvitedYet": "Not invited yet",
    "ui.people.ownerAndHouseholdAppearOnce":
      "Owner and household appear once the homelab identity provider (Pocket ID or TinyAuth) reports through the Guard.",
    "ui.people.plannedInTheWizard": "Planned in the Wizard",
    "ui.people.plannedMember": "Planned member",
    "ui.people.signedInToTheHomelab": "Signed in to the homelab",
    "ui.peoplePanel.canManageTheHomelab": "Can manage the homelab",
    "ui.peoplePanel.device": "Device",
    "ui.peoplePanel.household": "Household",
    "ui.peoplePanel.householdDevicesAreNotTracked":
      "Household devices are not tracked by kombify Connect.",
    "ui.peoplePanel.householdMember": "Household member",
    "ui.peoplePanel.kombifyClients": "kombify clients",
    "ui.peoplePanel.noHouseholdMembersYetInvite":
      "No household members yet. Invite them from the homelab identity setup.",
    "ui.peoplePanel.noKombifyClientIsLinked":
      "No kombify client is linked yet. Install the Techstack client, Companion or Workbench to see it here.",
    "ui.peoplePanel.nobodyInThisFilter": "Nobody in this filter.",
    "ui.peoplePanel.ownerDevicesCanManageThe":
      "Owner devices can manage the homelab; household members only use its apps.",
    "ui.peoplePanel.person": "Person",
    "ui.peoplePanel.platform": "Platform",
    "ui.peoplePanel.usesTheHomelabSApps": "Uses the homelab's apps",
    "ui.postMessageBridge.authTokenRequestTimedOut":
      "Auth token request timed out",
    "ui.postMessageBridge.authenticationFailed": "Authentication failed",
    "ui.postMessageBridge.bridgeDestroyed": "Bridge destroyed",
    "ui.postMessageBridge.gatewayAuthenticationFailed":
      "Gateway authentication failed",
    "ui.postMessageBridge.gatewayTokenRequestTimedOut":
      "Gateway token request timed out",
    "ui.postMessageBridge.kombifyAIDidNotConfirm":
      "kombify AI did not confirm the support session in time.",
    "ui.presets.aCalmOverviewOneBand":
      "A calm overview: one band per Node with its 24-hour curve.",
    "ui.presets.complete": "Complete",
    "ui.presets.nodeRowsWithTheirApps":
      "Node rows with their apps, devices and people beside them.",
    "ui.providerErrors.aStackKitWasSelectedE":
      "A StackKit was selected (e.g. basement-kit or cloud-kit), but the StackKit files are not available on the server.",
    "ui.providerErrors.anUnknownErrorOccurred": "An unknown error occurred.",
    "ui.providerErrors.baseStackkitNotFound": "base stackkit not found",
    "ui.providerErrors.baseSubdomainLimitReached":
      "base subdomain limit reached",
    "ui.providerErrors.bootstrapManagedRuntimeTarget":
      "bootstrap managed runtime target",
    "ui.providerErrors.checkCloudInitDockerStatus":
      "Check cloud-init, Docker status, and SSH reachability on the Managed Runtime server",
    "ui.providerErrors.checkPortConflictsInThe":
      "Check port conflicts in the error message",
    "ui.providerErrors.checkThatCUEIsInstalled":
      "Check that CUE is installed correctly",
    "ui.providerErrors.checkThatPort5260Is":
      "Check that port 5260 is reachable",
    "ui.providerErrors.checkThatStackKitFilesAre":
      "Check that StackKit files are present in the configured StackKits checkout",
    "ui.providerErrors.checkThatThePocketBaseDatabase":
      "Check that the PocketBase database is running",
    "ui.providerErrors.checkThatTheStackName":
      "Check that the stack name is valid (letters, numbers, and hyphens only)",
    "ui.providerErrors.checkThatTheSubmittedValues":
      "Check that the submitted values are plausible",
    "ui.providerErrors.checkTheBrowserConsoleFor":
      "Check the browser console for JavaScript errors",
    "ui.providerErrors.checkTheErrorDetailsFor":
      "Check the error details for kombify.me registration, quota, or StackKits CLI output",
    "ui.providerErrors.checkTheErrorDetailsFor2":
      "Check the error details for backend error, target bootstrap, and runtime diagnostics",
    "ui.providerErrors.checkTheLifecycleReceiptAnd":
      "Check the lifecycle receipt and automatic cleanup status; do not start another Node until definitive absence is confirmed",
    "ui.providerErrors.checkTheProviderErrorCode":
      "Check the provider error code in the error details",
    "ui.providerErrors.checkTheProviderPortalTo":
      "Check the provider portal to see whether the server is still starting or was rebooted",
    "ui.providerErrors.checkTheRuntimeActionResponse":
      "Check the Runtime Action response for stackkit_outputs.identity.owner.username",
    "ui.providerErrors.checkTheRuntimeActionResponse2":
      "Check the Runtime Action response for stackkit_outputs.login_gateway.url",
    "ui.providerErrors.checkTheRuntimeActionResponse3":
      "Check the Runtime Action response for stackkit_outputs.identity.recovery",
    "ui.providerErrors.checkTheRuntimeLogsFor":
      "Check the runtime logs for the same stack, job, lease, and provider",
    "ui.providerErrors.checkTheServerLogsDocker":
      "Check the server logs: 'docker compose logs techstack'",
    "ui.providerErrors.checkTheVMLeaseEnrollment":
      "Check the VM lease enrollment events and Sentry for the provider error",
    "ui.providerErrors.checkWhetherCentronOrIONOS":
      "Check whether Centron or IONOS actually created the server",
    "ui.providerErrors.checkWhetherTheServerStill":
      "Check whether the server still exists at Centron or IONOS",
    "ui.providerErrors.checkYourInternetConnection":
      "Check your internet connection",
    "ui.providerErrors.configurationCouldNotBeValidated":
      "Configuration could not be validated",
    "ui.providerErrors.createAGitHubIssueWith":
      "Create a GitHub issue with the reproduction steps",
    "ui.providerErrors.databaseError": "Database error",
    "ui.providerErrors.disableConflictingServices":
      "Disable conflicting services",
    "ui.providerErrors.doNotCreateAnotherProvider":
      "Do not create another provider VM; reuse the existing stack and lease for the next rollout attempt",
    "ui.providerErrors.doNotCreateAnotherProvider2":
      "Do not create another provider VM until the existing job has a diagnostic artifact or a clear skip reason",
    "ui.providerErrors.doNotRetryAdditionalNode":
      "Do not retry Additional Node against a v1 homelab",
    "ui.providerErrors.errorCode": "Error code: {code}",
    "ui.providerErrors.forAuthenticationErrorsMakeSure":
      "For authentication errors, make sure the passwords match",
    "ui.providerErrors.forCustomStackKitsValidateThe":
      "For custom StackKits: validate the CUE syntax",
    "ui.providerErrors.forMonitoringVictoriaMetricsRetentionRequires":
      "For monitoring: VictoriaMetrics retention requires persistent storage",
    "ui.providerErrors.forPersistentErrorsCreateA":
      "For persistent errors: create a GitHub issue with the logs",
    "ui.providerErrors.forVPNServicesOnlyOne":
      "For VPN services: only one VPN provider can be active at a time",
    "ui.providerErrors.foundANewKitFor":
      "Found a new kit for this server instead of joining the existing v1 deployment",
    "ui.providerErrors.ifProviderSupportIsRequired":
      "If provider support is required, include the error code from the details",
    "ui.providerErrors.ifRunningTheBinaryOutside":
      "If running the binary outside the repo: set TECHSTACK_STACKKITS_DIR to a published StackKits checkout",
    "ui.providerErrors.ifThisIsANew":
      "If this is a new homelab, found a kit instead of adding a Node",
    "ui.providerErrors.ifThisIsANew2":
      "If this is a new deployment, retry the Wizard so Techstack can project a stackkit/v2alpha1 spec",
    "ui.providerErrors.joinRequiresAnArchitectureV2":
      "join requires an architecture v2",
    "ui.providerErrors.joiningASecondController": "joining a second controller",
    "ui.providerErrors.kitDirectoryNotFound": "kit directory not found",
    "ui.providerErrors.kombifyMeRegistrationFailed":
      "kombify.me registration failed",
    "ui.providerErrors.makeSureAStackKitWas":
      "Make sure a StackKit was selected or detected",
    "ui.providerErrors.makeSureTheKombifyTechstack":
      "Make sure the kombify Techstack server is running",
    "ui.providerErrors.managedRuntimeCouldNotBe":
      "Managed Runtime could not be prepared",
    "ui.providerErrors.managedRuntimeCouldNotBe2":
      "Managed Runtime could not be created",
    "ui.providerErrors.managedRuntimeIsNotReady":
      "Managed Runtime is not ready yet",
    "ui.providerErrors.managedRuntimeTargetBootstrapFailed":
      "managed runtime target bootstrap failed",
    "ui.providerErrors.nameIsAlreadyInUse": "name is already in use",
    "ui.providerErrors.networkErrorWhileSaving": "Network error while saving",
    "ui.providerErrors.nextStepCheckLimitsQuota":
      "Next step: Check limits, quota, and running servers in the provider portal, then free the required resources.",
    "ui.providerErrors.nextStepCheckTheLifecycle":
      "Next step: Check the lifecycle receipt and automatic cleanup status. Retry only after definitive absence is confirmed.",
    "ui.providerErrors.nextStepCheckTheProvider":
      "Next step: Check the provider portal. If the error persists, send the error code to provider support.",
    "ui.providerErrors.nextStepWaitForThe":
      "Next step: Wait for the provider create/delete cooldown to end, then retry creation.",
    "ui.providerErrors.noCompatibleStackKitIsAvailable":
      "No compatible StackKit is available for the selected configuration.",
    "ui.providerErrors.noMatchingStackKitFound": "No matching StackKit found",
    "ui.providerErrors.noSubdomainprefixIsConfigured":
      "no subdomainprefix is configured",
    "ui.providerErrors.openTheLatestDestroyJob":
      "Open the latest destroy job for the provider's own error",
    "ui.providerErrors.providerMessage": "Provider message: {summary}",
    "ui.providerErrors.reloadThePageAndTry": "Reload the page and try again",
    "ui.providerErrors.resolveTheKombifyMeOr":
      "Resolve the kombify.me or StackKits blocker, then retry only the StackKit rollout",
    "ui.providerErrors.retryAdditionalNodeAFoundation":
      "Retry Additional Node. A Foundation role is joined as a worker on an existing kit",
    "ui.providerErrors.retryCreationOnlyAfterSSH":
      "Retry creation only after SSH is stable and Docker starts without errors",
    "ui.providerErrors.retryCreationOnlyAfterThe":
      "Retry creation only after the lease reports runtime_ssh_host or runtime_public_ip",
    "ui.providerErrors.retryOnlyAfterTheStackKit":
      "Retry only after the StackKit runtime action contract returns those outputs",
    "ui.providerErrors.retryTheRolloutOnlyAfter":
      "Retry the rollout only after SSH, Docker, and the StackKits Runtime Action are stable on the target server",
    "ui.providerErrors.reviewTheLogsWithDocker":
      "Review the logs with 'docker compose logs techstack'",
    "ui.providerErrors.reviewTheServerLogs": "Review the server logs",
    "ui.providerErrors.runtimeActionStackkitRollout":
      "runtime action stackkit_rollout",
    "ui.providerErrors.serviceConflictDetected": "Service conflict detected",
    "ui.providerErrors.stackkitArtifactGenerationFailed":
      "stackkit artifact generation failed",
    "ui.providerErrors.stackkitArtifactsCouldNotBe":
      "StackKit artifacts could not be generated",
    "ui.providerErrors.stackkitCliGenerateFailed":
      "stackkit cli generate failed",
    "ui.providerErrors.stackkitFilesMissing": "StackKit files missing",
    "ui.providerErrors.stackkitRolloutCouldNotBe":
      "StackKit rollout could not be applied",
    "ui.providerErrors.stackkitsArtifactGenerationFailed":
      "stackkits artifact generation failed",
    "ui.providerErrors.stackkitsCliGenerateFailed":
      "stackkits cli generate failed",
    "ui.providerErrors.stackkitsCouldNotApply": "stackkits could not apply",
    "ui.providerErrors.stackkitsOnlyAcceptsAnArchitecture":
      "StackKits only accepts an Architecture v2 StackSpec. A v1 document, or v2 fields such as useCases on a v1 document, cannot be validated or joined.",
    "ui.providerErrors.switchToADifferentAccess":
      "Switch to a different access mode (Home/Anywhere)",
    "ui.providerErrors.technicalDetails": "Technical details:",
    "ui.providerErrors.theAdditionalNodeIntentCould":
      "The Additional Node intent could not be projected onto the Architecture v2 kit spec.",
    "ui.providerErrors.theCloudProviderRejectedOr":
      "The cloud provider rejected or did not complete VM creation. The provider error is included in the details below.",
    "ui.providerErrors.theCloudProviderRejectedServer":
      "The cloud provider rejected server creation.",
    "ui.providerErrors.theConfigurationCouldNotBe":
      "The configuration could not be saved to the database.",
    "ui.providerErrors.theConfigurationCouldNotBe2":
      "The configuration could not be transformed into a valid deployment spec.",
    "ui.providerErrors.theKombifyTechstackServerCould":
      "The kombify Techstack server could not be reached.",
    "ui.providerErrors.theManagedVMWasPrepared":
      "The managed VM was prepared, but StackKits could not generate the rollout artifacts or kombify.me routing data.",
    "ui.providerErrors.theRolloutDidNotReturn":
      "The rollout did not return the owner login, login gateway, and recovery outputs required to use the stack.",
    "ui.providerErrors.theSelectedServicesHaveConflicting":
      "The selected services have conflicting requirements.",
    "ui.providerErrors.theSubmittedConfigurationContainsInvalid":
      "The submitted configuration contains invalid or missing values.",
    "ui.providerErrors.theTeardownStoppedBecauseTechstack":
      "The teardown stopped because Techstack could not match the request to an authoritative provider lease. Nothing was force-removed, so provider resources may still exist.",
    "ui.providerErrors.theVMIsReachableBut":
      "The VM is reachable, but Techstack could not prepare Docker or the bootstrap baseline reliably on the Managed Runtime server.",
    "ui.providerErrors.theVMLeaseHasNot":
      "The VM lease has not reported an SSH host or public IP. Creation stopped so the operation cannot remain in provisioning indefinitely.",
    "ui.providerErrors.theVMWasPreparedBut":
      "The VM was prepared, but the StackKits Runtime Action could not apply the selected StackKit rollout.",
    "ui.providerErrors.thisDeploymentCouldNotBe":
      "This deployment could not be decommissioned",
    "ui.providerErrors.thisDeploymentIsNotArchitecture":
      "This deployment is not Architecture v2",
    "ui.providerErrors.thisNodeCouldNotBe": "This Node could not be added",
    "ui.providerErrors.thisNodeCouldNotBe2": "this node could not be added",
    "ui.providerErrors.tooManyRecentCreateAnd":
      "too many recent create and delete operations",
    "ui.providerErrors.tooManyRecentCreateDelete":
      "too many recent create/delete operations",
    "ui.providerErrors.tryRestartingTheKombifyTechstack":
      "Try restarting the kombify-Techstack server",
    "ui.providerErrors.trySelectingFewerServices":
      "Try selecting fewer services",
    "ui.providerErrors.unexpectedError": "Unexpected error",
    "ui.providerErrors.unifierProcessingError": "Unifier processing error",
    "ui.providerErrors.useTheServerSForce":
      "Use the server's force decommission only after confirming the provider resources are gone",
    "ui.providerErrors.v1StackspecCannotCarry": "v1 stackspec cannot carry",
    "ui.providerErrors.validateTheStackSpecYaml":
      "Validate the stack-spec.yaml manually with the StackKits validator",
    "ui.providerErrors.verifyTheStackKitDirectoryExists":
      "Verify the StackKit directory exists on the server (e.g. /app/stackkits/basement-kit or /app/stackkits/cloud-kit)",
    "ui.providerErrors.verifyWritePermissionsForThe":
      "Verify write permissions for the pb_data/ directory",
    "ui.providerErrors.waitForRateLimitsOr":
      "Wait for rate limits or create/delete throttling to clear before retrying",
    "ui.providerErrors.withDockerEnsureTheVolume":
      "With Docker: ensure the volume is mounted correctly",
    "ui.providerErrors.withDockerRebuildTheImage":
      "With Docker: rebuild the image (ensures the pinned StackKits checkout is present)",
    "ui.providerErrors.withDockerUseDockerPs":
      "With Docker, use 'docker ps' to verify that all containers are running",
    "ui.providerErrors.withoutExitStatusOrExit":
      "without exit status or exit signal",
    "ui.recreate.confirmTitle": "Recreate {name}?",
    "ui.requirements.1KombifyCloudServerIs":
      "1 kombify Cloud server is provisioned automatically",
    "ui.requirements.aCloudServerIsRequired":
      "A cloud server is required for external access",
    "ui.requirements.aKombifyCloudServerIs":
      "A kombify Cloud server is provisioned automatically through the subscription",
    "ui.requirements.aLocalServerIsRequired":
      "A local server is required for homelab services",
    "ui.requirements.anExistingRemoteServerIs":
      "An existing remote server is connected over SSH",
    "ui.requirements.atLeast1ExistingServer": "At least 1 existing server",
    "ui.requirements.atLeast1UserOwned": "At least 1 user-owned server",
    "ui.requirements.atLeastCloudServer":
      "At least {minCloudServers} cloud server",
    "ui.requirements.atLeastCloudServerAnd":
      "At least {minCloudServers} cloud server and {minLocalServers} local server",
    "ui.requirements.atLeastLocalServer":
      "At least {minLocalServers} local server",
    "ui.requirements.completeSetupAtLeast1":
      "Complete setup: At least 1 capable server",
    "ui.requirements.completeSetupKombifyCloudServer":
      "Complete setup: kombify Cloud server with sufficient resources",
    "ui.requirements.dockerContainer": "Docker container",
    "ui.requirements.homeAssistantOSCanRun":
      "Home Assistant OS can run in a separate appliance VM on the same hypervisor.",
    "ui.requirements.hybridSetupCloudAndLocal":
      "Hybrid setup: Cloud and local servers are connected",
    "ui.requirements.linuxRecommendedInstallsThePersistent":
      "Linux (recommended) — installs the persistent outbound Guard through the Core/API URL (/install.sh). If running on another host/VM, replace localhost with the reachable IP/domain of your kombify-Techstack server.",
    "ui.requirements.manualInstallationAfterBinaryDownload":
      "Manual installation (after binary download)",
    "ui.requirements.techstackPreparesTheVMThen":
      "Techstack prepares the VM, then continues the standard StackKits installation.",
    "ui.requirements.theInstallationCommandRunsOn":
      "The installation command runs on your own server or device",
    "ui.requirements.ubuntuGuestOnYourProxmox":
      "Ubuntu guest on your Proxmox hypervisor",
    "ui.rotation.lastEvery": "Last: {last} • Every {days}d",
    "ui.rotation.needAttention": "{count} need attention",
    "ui.rotationReminders.daysAgo.one": "{count} day ago",
    "ui.rotationReminders.daysAgo.other": "{count} days ago",
    "ui.rotationReminders.monthsAgo.one": "{count} month ago",
    "ui.rotationReminders.monthsAgo.other": "{count} months ago",
    "ui.rotationReminders.never": "Never",
    "ui.rotationReminders.overdue": "Overdue",
    "ui.rotationReminders.recommendedRotationApiKeys90d":
      "Recommended rotation: API Keys (90d), Passwords (180d), OAuth Tokens (30d), Certificates (365d)",
    "ui.rotationReminders.setupNeeded": "Setup needed",
    "ui.rotationReminders.today": "Today",
    "ui.rotationReminders.yesterday": "Yesterday",
    "ui.sSHKeyGenerator.addThisToSshAuthorized":
      "Add this to ~/.ssh/authorized_keys on your servers",
    "ui.sSHKeyGenerator.algorithm": "Algorithm",
    "ui.sSHKeyGenerator.algorithm2": "Algorithm:",
    "ui.sSHKeyGenerator.close": "Close",
    "ui.sSHKeyGenerator.downloadAndSecurelyStoreYour":
      "Download and securely store your private key now. It cannot be recovered if lost. Never share your private key.",
    "ui.sSHKeyGenerator.downloadPub": "Download .pub",
    "ui.sSHKeyGenerator.eGProductionServerGithub":
      "e.g., Production Server, GitHub Deploy",
    "ui.sSHKeyGenerator.ed25519IsRecommendedForMost":
      "Ed25519 is recommended for most use cases. Use RSA only if you need compatibility with older systems.",
    "ui.sSHKeyGenerator.failedToSaveKey": "Failed to save key",
    "ui.sSHKeyGenerator.fingerprint": "Fingerprint:",
    "ui.sSHKeyGenerator.generate": "Generate",
    "ui.sSHKeyGenerator.generating": "Generating...",
    "ui.sSHKeyGenerator.keyGenerated": "Key Generated",
    "ui.sSHKeyGenerator.keyGenerationFailed": "Key generation failed",
    "ui.sSHKeyGenerator.keyName": "Key Name",
    "ui.sSHKeyGenerator.keyPairGeneratedSuccessfully":
      "Key pair generated successfully",
    "ui.sSHKeyGenerator.maximumCompatibilityStronger":
      "Maximum compatibility, stronger",
    "ui.sSHKeyGenerator.modernFastSecureRecommended":
      "Modern, fast, secure (recommended)",
    "ui.sSHKeyGenerator.name": "Name:",
    "ui.sSHKeyGenerator.pleaseEnterANameFor":
      "Please enter a name for this key",
    "ui.sSHKeyGenerator.privateKey": "Private Key",
    "ui.sSHKeyGenerator.publicKey": "Public Key",
    "ui.sSHKeyGenerator.saveToWallet": "Save to Wallet",
    "ui.sSHKeyGenerator.sshKeyGenerationRequiresHttps":
      "SSH key generation requires HTTPS and a modern browser with Web Crypto API support.",
    "ui.sSHKeyGenerator.tip": "Tip:",
    "ui.sSHKeyGenerator.wideCompatibilityStandardStrength":
      "Wide compatibility, standard strength",
    "ui.serverAccess.installKeyConfirm":
      'Install public key "{key}" once on {server}? The private key never leaves the Wallet.',
    "ui.serverAccess.sshKey": "SSH key",
    "ui.serverAccessActions.addOneInTheWallet": "Add one in the Wallet",
    "ui.serverAccessActions.authorizeOneOfYourPublic":
      "Authorize one of your public keys on this Node first.",
    "ui.serverAccessActions.authorizeSshKey": "Authorize SSH key",
    "ui.serverAccessActions.copySshCommand": "Copy SSH command",
    "ui.serverAccessActions.installPublicKey": "Install public key",
    "ui.serverAccessActions.noSshKeyInThe": "No SSH key in the Wallet yet.",
    "ui.serverAccessActions.openTerminal": "Open terminal",
    "ui.serverAccessActions.sshCommandIsUnavailable":
      "SSH command is unavailable",
    "ui.serverAccessActions.terminalAccessIsUnavailable":
      "Terminal access is unavailable",
    "ui.serverAccessActions.thisCommandRequiresOneOf":
      "This command requires one of your previously authorized SSH keys.",
    "ui.serverAccessActions.walletPublicKey": "Wallet Public Key",
    "ui.serverCard.archUnknown": "arch unknown",
    "ui.serverCard.osUnknown": "os unknown",
    "ui.serverCardAdapter.anotherNodeActionIsRunning":
      "Another node action is running",
    "ui.serverDetail.actionNotStarted": "{action} could not be started.",
    "ui.serverDetail.actionsTarget":
      "These actions target {name} directly. The available controls follow its current connection and StackKit state; no server or lifecycle status needs to be selected.",
    "ui.serverDetail.confirmActionFor": "Confirm “{action}” for {name}",
    "ui.serverDetail.confirmDecommissionFor": "Confirm decommission for {name}",
    "ui.serverDetail.confirmDetachFor": "Confirm detach for {name}",
    "ui.serverDetail.confirmProviderRemoval":
      "Confirm provider removal for {name}",
    "ui.serverDetail.connectionState": "Server connection: {state}",
    "ui.serverDetail.decommissionIntro":
      "Request the controlled removal of {slot}. Techstack keeps the provider custody record until absence is verified. This action is intentionally available only here.",
    "ui.serverDetail.detachIntro":
      "Revoke the exact Guard Agent for {slot} and remove this attachment from current inventory. Techstack keeps the terminal audit receipt and performs no provider API call.",
    "ui.serverDetail.evidence": "Evidence: {sources}",
    "ui.serverDetail.healthSource":
      "Source: {source}. Monitoring backend: {backend}.",
    "ui.serverDetail.jobAccepted": "Job {id} was accepted for this server.",
    "ui.serverDetail.jobLabel": "Job {id}",
    "ui.serverDetail.observedAt": "Observed {time}",
    "ui.serverDetail.sourceNotReported": "source not reported",
    "ui.serverLifecycleActions.applyPlan": "Apply plan",
    "ui.serverLifecycleActions.applyThePreparedStackKitPlan":
      "Apply the prepared StackKit plan to this server.",
    "ui.serverLifecycleActions.compareTheRunningServerWith":
      "Compare the running server with its desired StackKit state.",
    "ui.serverLifecycleActions.detectDrift": "Detect drift",
    "ui.serverLifecycleActions.planChanges": "Plan changes",
    "ui.serverLifecycleActions.previewTheNextStackKitChange":
      "Preview the next StackKit change for this server.",
    "ui.serverLifecycleActions.reconcileDrift": "Reconcile drift",
    "ui.serverLifecycleActions.restoreTheDesiredStackKitState":
      "Restore the desired StackKit state on this server.",
    "ui.serverLifecycleActions.upgradeThroughThePublishedStackKits":
      "Upgrade through the published StackKits release channel.",
    "ui.serverLifecycleActions.upgradeToLatest": "Upgrade to latest",
    "ui.serverLifecycleActions.verifyInstallation": "Verify installation",
    "ui.serverLifecycleActions.verifyReleaseReceiptOwnerBinding":
      "Verify release receipt, Owner binding, and runtime state.",
    "ui.serverList.inventoryUnavailable": "Inventory unavailable",
    "ui.serverList.theCanonicalInventoryIsUnavailable":
      "The canonical inventory is unavailable. Showing only telemetry that is currently authorized.",
    "ui.serverOutcome.continueOnConnected":
      "Continue StackKit on connected Node",
    "ui.serverOutcome.continuePrepOnConnected":
      "Continue StackKit preparation on connected Node",
    "ui.serverOutcome.restartRollout": "Restart rollout",
    "ui.serverOutcome.retrySsh": "Retry SSH connection",
    "ui.serverProvisioningStep.cloudKitRollout": "Cloud Kit rollout",
    "ui.serverRegistry.addOneToHomelab": "add one to this homelab",
    "ui.serverRegistry.joinSurface":
      "This Node joins the existing StackKit deployment, which already has its Foundation Node. A second main Node runs its own StackKit deployment — {slot}.",
    "ui.serverRegistryPanel.everyRegisteredServerIsBound":
      "Every registered server is bound to one concrete StackKit foundation.",
    "ui.serverRegistryPanel.foundationNodeIsTheProduct":
      "Foundation Node is the product label for the first/core server.",
    "ui.serverRegistryPanel.optionalServices": "Optional services",
    "ui.serverRegistryPanel.serverRole": "Server role",
    "ui.serverRegistryPanel.stackkitFoundation": "StackKit foundation",
    "ui.serverRegistryPanel.theseSelectionsFeedTheSame":
      "These selections feed the same Service Registry contract used by Service Management.",
    "ui.serverRegistryPanel.thisNodeJoinsTheExisting":
      "This Node joins the existing StackKit. The foundation is already bound.",
    "ui.serverTerminalModal.invalidTerminalStreamResponse":
      "Invalid terminal stream response",
    "ui.serverTerminalModal.terminalConnectionFailed":
      "Terminal connection failed",
    "ui.serviceCardAdapter.anotherGovernedActionIsRunning":
      "Another governed action is running",
    "ui.serviceCardAdapter.archivedSourceService": "Archived source service.",
    "ui.serviceCardAdapter.endpointIsReachableButProtected":
      "Endpoint is reachable, but protected service health is not verified.",
    "ui.serviceCardAdapter.locked": "Locked",
    "ui.serviceCardAdapter.lockedBy": "Locked by {actor}",
    "ui.serviceCardAdapter.noCurrentRuntimeObservationIs":
      "No current runtime observation is available.",
    "ui.serviceCardAdapter.observedOnlyAdoptToManage":
      "Observed only — adopt to manage lifecycle.",
    "ui.serviceCardAdapter.serviceReportedAnError":
      "Service reported an error.",
    "ui.serviceCardAdapter.waitingForADockerHealth":
      "Waiting for a Docker health or endpoint probe.",
    "ui.serviceCardAdapter.waitingForPlacement": "Waiting for placement.",
    "ui.serviceDiscovery.adding": "Adding...",
    "ui.serviceDiscovery.deselectAll": "Deselect All",
    "ui.serviceDiscovery.discoveryFailed": "Discovery failed",
    "ui.serviceDiscovery.failedToAddCredentials": "Failed to add credentials",
    "ui.serviceDiscovery.noUrl": "No URL",
    "ui.serviceDiscovery.selectAll": "Select All",
    "ui.serviceList.servicesByRuntimeTarget": "Services by runtime target",
    "ui.serviceRegistry.selected.one": "{count} service selected",
    "ui.serviceRegistry.selected.other": "{count} services selected",
    "ui.serviceSheet.node": "Node: {node}",
    "ui.serviceSheet.notPlaced": "not placed",
    "ui.serviceSheet.ownership": "Ownership: {value}",
    "ui.serviceSheet.stackkit": "StackKit {version}",
    "ui.services.aRegisteredNodeIsRequired":
      "A registered Node is required before services can be attached.",
    "ui.services.aboutToMove":
      "You are about to move this application from {slot} to {slot}.",
    "ui.services.action.freeze": "Freeze",
    "ui.services.action.logs": "Logs",
    "ui.services.action.restart": "Restart",
    "ui.services.action.start": "Start",
    "ui.services.action.stop": "Stop",
    "ui.services.action.unfreeze": "Unfreeze",
    "ui.services.actionCompletedAndFreshInventory":
      "Action completed and fresh inventory was observed.",
    "ui.services.actionCompletedButNoNewer":
      "Action completed, but no newer inventory observation arrived within the service freshness window.",
    "ui.services.actionCompletedInventoryIsRefreshing":
      "Action completed; inventory is refreshing.",
    "ui.services.actionTitle": "{action} {name}",
    "ui.services.addACatalogApplicationOr":
      "Add a catalog application or import unmanaged Node inventory.",
    "ui.services.addANewApplication": "Add a new application",
    "ui.services.addService": "Add Service",
    "ui.services.adoptIntoManagement": "Adopt into management…",
    "ui.services.all": "All",
    "ui.services.anyRuntimeInventoryShownAbove":
      "Any runtime inventory shown above remains read-only. Catalog, import, verification, and migration actions stay disabled until the service registry recovers.",
    "ui.services.application": "Application",
    "ui.services.applicationIsAlreadyMovingOr":
      "Application is already moving or waiting for verification.",
    "ui.services.applicationManagementIsTemporarilyUnavailable":
      "Application management is temporarily unavailable",
    "ui.services.applicationManagementIsUnavailableUntil":
      "Application management is unavailable until the service registry recovers.",
    "ui.services.applicationMove": "Application Move",
    "ui.services.applicationNeedsAStableRunning":
      "Application needs a stable running or stopped state before it can be moved.",
    "ui.services.applicationPlacementIsTemporarilyUnavailable":
      "Application placement is temporarily unavailable",
    "ui.services.applications": "Applications",
    "ui.services.applicationsAppearAfterStackkitsRuntime":
      "Applications appear after StackKits runtime facts are observed.",
    "ui.services.applicationsShown.one": "{shown} shown · {count} application",
    "ui.services.applicationsShown.other":
      "{shown} shown · {count} applications",
    "ui.services.approveAction": "Approve {action}",
    "ui.services.capacityTelemetryRefreshFailed":
      "Capacity telemetry refresh failed",
    "ui.services.catalog": "Catalog",
    "ui.services.catalogServiceCouldNotBe":
      "Catalog service could not be added.",
    "ui.services.clearTheFilterToShow":
      "Clear the filter to show all applications.",
    "ui.services.cloudVps": "Cloud VPS",
    "ui.services.componentCount.one": "{count} component",
    "ui.services.componentCount.other": "{count} components",
    "ui.services.components": "Components",
    "ui.services.componentsOn.one": "{count} component on {server}",
    "ui.services.componentsOn.other": "{count} components on {server}",
    "ui.services.confirmGoverned":
      "Approve the governed {action} of {name}? A StackKits verify pass runs after the mutation before measured state changes.",
    "ui.services.confirmLock":
      "Lock {name}? Start, stop and restart are refused while it is locked, including through a stack apply. The service keeps running.",
    "ui.services.confirmUnlock":
      "Unlock {name}? Governed mutations become available again.",
    "ui.services.connectNodesToYourHomelab":
      "Connect Nodes to your Homelab to manage application placement.",
    "ui.services.deleteArchivedService": "Delete archived service?",
    "ui.services.desired": "Desired",
    "ui.services.desiredObserved": "Desired {desired} · Observed {observed}",
    "ui.services.discoveredOn": "Discovered on {name} — not managed by kombify",
    "ui.services.displayName": "Display name",
    "ui.services.dragAndDropStaysDisabled":
      "Drag-and-drop stays disabled until deployment, health verification, cutover, and source drain are handled by a real runtime executor.",
    "ui.services.dropManagedApplicationsHere": "Drop managed applications here",
    "ui.services.error": "Error",
    "ui.services.failedToDeleteArchivedService":
      "Failed to delete archived service.",
    "ui.services.failedToInitiateMigration": "Failed to initiate migration.",
    "ui.services.failedToVerifyService": "Failed to verify service.",
    "ui.services.fetchLogs": "Fetch logs",
    "ui.services.freshnessValue": "Freshness: {value}",
    "ui.services.health": "Health",
    "ui.services.importAsObserved": "Import as Observed",
    "ui.services.importUnmanaged": "Import unmanaged",
    "ui.services.internalAddressOnlyOpenIt":
      "Internal address only; open it from the Node network.",
    "ui.services.jobId": "job {id}",
    "ui.services.loadOlderLogs": "Load older logs",
    "ui.services.loadingCanonicalServiceInventory":
      "Loading canonical service inventory",
    "ui.services.localNode": "Local Node",
    "ui.services.managedProviderTarget": "Managed provider target",
    "ui.services.migrationJobIsActive": "Migration job is active.",
    "ui.services.moveApplication": "Move Application",
    "ui.services.moveHere": "Move {name} here",
    "ui.services.moveSteps": "Move Steps:",
    "ui.services.moveToAnotherNode": "Move to another Node…",
    "ui.services.mutationsRequireOwnerApprovalA":
      "Mutations require owner approval; a StackKits verify pass runs before measured state changes.",
    "ui.services.newApplication": "New Application",
    "ui.services.noApplicationsDeployed": "No applications deployed",
    "ui.services.noApplicationsMatching": "No applications matching {status}",
    "ui.services.noApplicationsReported": "No applications reported",
    "ui.services.noAvailableTargetNodeFor":
      "No available target Node for this application.",
    "ui.services.noCanonicalRuntimeObservationYet":
      "No canonical runtime observation yet — registry projection shown.",
    "ui.services.noGovernedActionHasRun":
      "No governed action has run in this session.",
    "ui.services.noHostname": "No hostname",
    "ui.services.noNodesRegistered": "No Nodes registered",
    "ui.services.noOperationsAvailable": "No operations available.",
    "ui.services.noSystemServices": "No system services",
    "ui.services.node": "Node",
    "ui.services.nodeBoundApplications": "Node-bound applications",
    "ui.services.nodeCapabilities": "Node capabilities",
    "ui.services.nodeColumn": "Node {name} column",
    "ui.services.observed": "Observed",
    "ui.services.observedTechnicalComponentsWithoutApplication":
      "Observed technical components without application-level controls",
    "ui.services.observedUnmanagedApplicationsMustBe":
      "Observed unmanaged applications must be adopted before they can be moved.",
    "ui.services.open": "Open",
    "ui.services.operationValue": "Operation: {value}",
    "ui.services.ownershipUnknown": "Ownership unknown",
    "ui.services.pending": "Pending",
    "ui.services.placement.cloud": "Cloud",
    "ui.services.placement.local": "Local",
    "ui.services.placement.managed": "Managed workload",
    "ui.services.placement.unknown": "Placement unknown",
    "ui.services.placementAndMigrationControlsStay":
      "Placement and migration controls stay disabled until the service registry recovers.",
    "ui.services.placementBoard": "Placement Board",
    "ui.services.placementUnknown": "Placement unknown",
    "ui.services.port": "Port",
    "ui.services.ramUsedOf": "{used} GB / {total} GB",
    "ui.services.refreshLogs": "Refresh logs",
    "ui.services.relocatingOnBackend": "Relocating on backend...",
    "ui.services.removeOldCopy": "Remove old copy…",
    "ui.services.retryManagementData": "Retry management data",
    "ui.services.running": "Running",
    "ui.services.runtimeInventoryCouldNotBe":
      "Runtime inventory could not be loaded.",
    "ui.services.runtimeMigrationIsNotEnabled":
      "Runtime migration is not enabled yet",
    "ui.services.runtimeMigrationUnavailable": "Runtime migration unavailable",
    "ui.services.runtimeServiceMigrationIsNot":
      "Runtime service migration is not enabled on this deployment.",
    "ui.services.runtimeTarget": "Runtime target",
    "ui.services.selectANodeAndCatalog": "Select a node and catalog service.",
    "ui.services.selectANodeThenEnter":
      "Select a node, then enter a service name.",
    "ui.services.selected": "Selected: {name}",
    "ui.services.service": "Service",
    "ui.services.serviceActionDidNotComplete":
      "Service action did not complete.",
    "ui.services.serviceActionDidNotReturn":
      "Service action did not return a job ID.",
    "ui.services.serviceActionFailed": "Service action failed.",
    "ui.services.serviceFallback": "service",
    "ui.services.serviceLogCollectionDidNot":
      "Service log collection did not complete.",
    "ui.services.serviceLogsCouldNotBe": "Service logs could not be loaded.",
    "ui.services.serviceMode": "Service mode",
    "ui.services.serviceRegistryCouldNotBe":
      "Service Registry could not be loaded.",
    "ui.services.services": "Services",
    "ui.services.servicesMode": "Services mode",
    "ui.services.showingLastVerifiedCapacityTelemetry":
      "Showing last verified capacity telemetry",
    "ui.services.stackkitsApplicationModel": "StackKits application model",
    "ui.services.startMove": "Start Move",
    "ui.services.startsControlledMove":
      "This starts a controlled move of {slot}.",
    "ui.services.statusValue": "Status: {value}",
    "ui.services.storage": "Storage",
    "ui.services.storageFreeOf": "{free} GB free / {total} GB",
    "ui.services.systemServices": "System Services",
    "ui.services.targetIsReadyForVerification":
      "Target is ready for verification.",
    "ui.services.targetNode": "Target Node",
    "ui.services.targetValue": "Target: {value}",
    "ui.services.telemetryPending": "telemetry pending",
    "ui.services.temporaryInstance":
      "A temporary instance will be deployed on {slot}.",
    "ui.services.theApplicationConfigWillBe":
      "The application config will be duplicated on the target Node.",
    "ui.services.theLastLogPageWas": "The last log page was empty.",
    "ui.services.theLastSuccessfulSnapshotRemains":
      "The last successful snapshot remains visible until this StackKit deployment reports replacement or explicit down evidence.",
    "ui.services.thisHomelab": "this homelab",
    "ui.services.thisPermanentlyDeletesTheArchived":
      "This permanently deletes the archived service from its source Node.",
    "ui.services.type": "Type:",
    "ui.services.type2": "Type",
    "ui.services.unclassifiedRuntimeComponentsAppearHere":
      "Unclassified runtime components appear here.",
    "ui.services.unmanagedServiceCouldNotBe":
      "Unmanaged service could not be imported.",
    "ui.services.uponYourManualVerificationThe":
      "Upon your manual verification, the old instance is deactivated and can be permanently removed.",
    "ui.services.url": "URL",
    "ui.services.verifyFinish": "Verify & Finish",
    "ui.services.verifying": "· verifying",
    "ui.services.waitingForRuntimeDeployment":
      "Waiting for runtime deployment...",
    "ui.services.youCanTestTheNew":
      "You can test the new instance while the old one remains active.",
    "ui.servicesApplicationId.addressClass": "Address class",
    "ui.servicesApplicationId.applicationDetails": "Application details",
    "ui.servicesApplicationId.applicationDetailsCouldNotBe":
      "Application details could not be loaded.",
    "ui.servicesApplicationId.applicationKey": "Application key",
    "ui.servicesApplicationId.backToApplications": "Back to applications",
    "ui.servicesApplicationId.freshness": "Freshness",
    "ui.servicesApplicationId.impact": "Impact",
    "ui.servicesApplicationId.kitDeployment": "Kit deployment",
    "ui.servicesApplicationId.lifecycle": "Lifecycle",
    "ui.servicesApplicationId.management": "Management",
    "ui.servicesApplicationId.noReachableAddressReported":
      "No reachable address reported",
    "ui.servicesApplicationId.openService": "Open service",
    "ui.servicesApplicationId.role": "Role",
    "ui.servicesApplicationId.runtimeAssignment": "Runtime assignment",
    "ui.servicesApplicationId.server": "Server",
    "ui.servicesApplicationId.source": "Source",
    "ui.settings.account": "Account",
    "ui.settings.appearance": "Appearance",
    "ui.settings.appearanceMode": "Appearance mode",
    "ui.settings.applyExactPlan": "Apply exact plan",
    "ui.settings.applying": "Applying...",
    "ui.settings.cleanUpTestResidue": "Clean up test residue",
    "ui.settings.cleanupFailed": "Cleanup failed",
    "ui.settings.cleanupReviewFailed": "Cleanup review failed",
    "ui.settings.dangerZone": "Danger Zone",
    "ui.settings.dashboard": "Dashboard",
    "ui.settings.dashboardLayout": "Dashboard layout",
    "ui.settings.decommissionsTheDeploymentSManaged":
      "Decommissions the deployment's managed runtime and removes its entry. To remove a single server instead, use its decommission action on the server.",
    "ui.settings.delete": "Delete",
    "ui.settings.deleteADeployment": "Delete a deployment",
    "ui.settings.deleteNamed": 'Delete "{name}"?',
    "ui.settings.deleteThisDeployment": "Delete this deployment",
    "ui.settings.deleting": "Deleting...",
    "ui.settings.deletionFailed": "Deletion failed",
    "ui.settings.failedToLoadDeployments": "Failed to load deployments",
    "ui.settings.failedToLoadHomelabIdentity":
      "Failed to load Homelab identity",
    "ui.settings.failedToSaveHomelabIdentity":
      "Failed to save Homelab identity",
    "ui.settings.flag": "Flag",
    "ui.settings.float": "Float",
    "ui.settings.followAccount": "Follow account",
    "ui.settings.followFinish": "Follow finish",
    "ui.settings.homelabIdentity": "Homelab Identity",
    "ui.settings.lightOrDarkAndThe":
      "Light or dark, and the surface finish. The finish follows your kombify account default until this device picks its own.",
    "ui.settings.loadingDeployments": "Loading deployments...",
    "ui.settings.logout": "Logout",
    "ui.settings.managedByKombifyCloud": "Managed by kombify Cloud.",
    "ui.settings.mode": "Mode",
    "ui.settings.navigationPreview": "Navigation preview",
    "ui.settings.navigationPreviewShape": "Navigation preview shape",
    "ui.settings.noDeploymentsToDelete": "No deployments to delete.",
    "ui.settings.noHomelabIdentityConfigured":
      "No Homelab identity configured.",
    "ui.settings.noVerifiedTestResidueRemains":
      "No verified test residue remains.",
    "ui.settings.noVerifiedTestResidueWas":
      "No verified test residue was found.",
    "ui.settings.overlap": "Overlap",
    "ui.settings.pruneQualifies":
      "Only authenticated-owner {owner} and strict {strict} projections qualify. Active Managed Runtimes, provider resources, recent Nodes, demo data, and normal Homelab data remain unchanged.",
    "ui.settings.reviewCleanup": "Review cleanup",
    "ui.settings.reviewVerifiedTestResidue": "Review verified test residue",
    "ui.settings.reviewing": "Reviewing...",
    "ui.settings.reviewsExactOwnerScopedE2e":
      "Reviews exact owner-scoped E2E and failed runtime projections. Provider resources, active leases, recent Nodes, and normal Homelab data remain unchanged.",
    "ui.settings.savedOnDevice": "Saved on this device for your account.",
    "ui.settings.settings": "Settings",
    "ui.settings.signedIn": "Signed in",
    "ui.settings.surface": "Surface",
    "ui.settings.surfaceFinish": "Surface finish",
    "ui.settings.tether": "Tether",
    "ui.settings.theDemoDeploymentIsProtected":
      "The demo deployment is protected and cannot be deleted.",
    "ui.settings.thisDecommissionsTheManagedRuntime":
      "This decommissions the managed runtime and removes the deployment entry. It cannot be undone.",
    "ui.settings.thisEntryIsNotVerified":
      "This entry is not verified test residue and cannot be removed by projection cleanup.",
    "ui.settings.thisExactPlanIsBound":
      "This exact plan is bound to the digest below. No infrastructure is destroyed.",
    "ui.settings.typeToConfirm": "Type {slot} to confirm",
    "ui.shortcuts.closeDialogDeselect": "Close dialog/deselect",
    "ui.shortcuts.focusSearchInput": "Focus search input",
    "ui.shortcuts.goToHomeDashboard": "Go to Home/Dashboard",
    "ui.shortcuts.goToMonitoring": "Go to Monitoring",
    "ui.shortcuts.goToServices": "Go to Services",
    "ui.shortcuts.goToSettings": "Go to Settings",
    "ui.shortcuts.goToWallet": "Go to Wallet",
    "ui.shortcuts.jumpToFirstItem": "Jump to first item",
    "ui.shortcuts.jumpToLastItem": "Jump to last item",
    "ui.shortcuts.moveDownInList": "Move down in list",
    "ui.shortcuts.moveUpInList": "Move up in list",
    "ui.shortcuts.pressToClose": "Press {slot} to close",
    "ui.shortcuts.pressToShow": "Press {slot} anytime to show this help",
    "ui.shortcuts.refreshCurrentView": "Refresh current view",
    "ui.shortcuts.selectOpenItem": "Select/open item",
    "ui.shortcuts.showKeyboardShortcutsHelp": "Show keyboard shortcuts help",
    "ui.shortcutsHelp.dialogs": "Dialogs",
    "ui.shortcutsHelp.esc": "Esc",
    "ui.shortcutsHelp.listNavigation": "List Navigation",
    "ui.shortcutsHelp.pageNavigation": "Page Navigation",
    "ui.sidebarNav.kombifyTechstack": "kombify-Techstack",
    "ui.sshKeygen.ed25519NotSupportedInThis":
      "Ed25519 not supported in this browser. Try RSA instead.",
    "ui.sshKeygen.unsupportedAlgorithm": "Unsupported algorithm: {algorithm}",
    "ui.stackImport.exportIntro":
      "Export this StackKit deployment configuration as {file}. You can edit the file and import it again later.",
    "ui.stackImport.intro":
      "Import a {file} file to set up a StackKit deployment in your Homelab. Legacy {legacy} files are still accepted.",
    "ui.stackImport.orPaste": "or paste",
    "ui.stackImport.validating": "Validating...",
    "ui.stackImportExportModal.allRequiredFieldsArePresent":
      "All required fields are present. After import, the Unifier process will start and determine the appropriate StackKit for your configuration.",
    "ui.stackImportExportModal.configurationErrors": "Configuration errors",
    "ui.stackImportExportModal.configurationIsValid": "Configuration is valid",
    "ui.stackImportExportModal.copyDiagnostics": "Copy Diagnostics",
    "ui.stackImportExportModal.copyDiagnosticsForDevelopers":
      "Copy Diagnostics (for developers)",
    "ui.stackImportExportModal.couldNotReadFile": "Could not read file",
    "ui.stackImportExportModal.dragFileHereOr": "Drag file here or",
    "ui.stackImportExportModal.exportStackkitDeploymentSpec":
      "Export StackKit deployment spec",
    "ui.stackImportExportModal.ifTheProblemPersistsCopy":
      "If the problem persists: copy the diagnostics and share them with developers.",
    "ui.stackImportExportModal.importStackkitDeploymentSpec":
      "Import StackKit deployment spec",
    "ui.stackImportExportModal.importStartSetup": "Import & Start Setup",
    "ui.stackImportExportModal.importingConfiguration":
      "Importing configuration...",
    "ui.stackImportExportModal.metadataAndIntents": "• Metadata and intents",
    "ui.stackImportExportModal.noContentFound": "No content found",
    "ui.stackImportExportModal.noContentToImport": "No content to import",
    "ui.stackImportExportModal.noStackkitDeploymentIsAvailable":
      "No StackKit deployment is available to export",
    "ui.stackImportExportModal.nodeDefinitionsWithoutCredentials":
      "• Node definitions (without credentials)",
    "ui.stackImportExportModal.pasteYourStackSpecYaml":
      "# Paste your stack-spec.yaml here...",
    "ui.stackImportExportModal.recommended": "(recommended)",
    "ui.stackImportExportModal.selectFile": "Select File",
    "ui.stackImportExportModal.serviceConfigurations":
      "• Service configurations",
    "ui.stackImportExportModal.sshKeysAndSecretsAre":
      "SSH keys and secrets are not exported. These must be reconfigured after an import.",
    "ui.stackImportExportModal.stackNameAndConfiguration":
      "• Stack name and configuration",
    "ui.stackImportExportModal.supportedYamlYmlJson":
      "Supported: .yaml, .yml, .json",
    "ui.stackImportExportModal.systemNetworkAndSecuritySettings":
      "• System, network, and security settings",
    "ui.stackImportExportModal.tipIfYouSeeAn":
      "Tip: If you see an HTML page instead of JSON, the backend proxy or your session/auth may be broken. Please try again.",
    "ui.stackImportExportModal.validateConfiguration": "Validate Configuration",
    "ui.stackImportExportModal.validationFailed": "Validation failed",
    "ui.stackImportExportModal.whatWillBeExported": "What will be exported?",
    "ui.stackImportExportModal.youWillBeRedirectedTo":
      "You will be redirected to the setup page shortly.",
    "ui.stacksCreating.creatingStackkitDeploymentKombifyTechstack":
      "Creating StackKit deployment | kombify-Techstack",
    "ui.stacksCreating.openOperations": "Open operations",
    "ui.stacksCreating.reviewAndStartStackkitRollout":
      "Review and start StackKit rollout",
    "ui.stacksCreatingCreation-controller.additionalNode": "Additional Node",
    "ui.stacksCreatingCreation-controller.afterFailedAttemptsTheBackend":
      "After {MAX_POLL_ERRORS} failed attempts, the backend could not be reached. The page keeps retrying with backoff; you can also check your network connection and reload to resume.",
    "ui.stacksCreatingCreation-controller.configurationPrepared":
      "Configuration prepared",
    "ui.stacksCreatingCreation-controller.connectYourNodeToContinue":
      "Connect your Node to continue the rollout",
    "ui.stacksCreatingCreation-controller.connectionToServerLost":
      "Connection to server lost",
    "ui.stacksCreatingCreation-controller.continueStackKitDeploymentOnConnected":
      "Continue StackKit deployment on connected Node",
    "ui.stacksCreatingCreation-controller.continueStackKitOnConnectedNode":
      "Continue StackKit on connected Node",
    "ui.stacksCreatingCreation-controller.continueStackKitRolloutOnConnected":
      "Continue StackKit rollout on connected Node",
    "ui.stacksCreatingCreation-controller.couldNotCheckRemoteSSH":
      "Could not check remote SSH enrollment progress.",
    "ui.stacksCreatingCreation-controller.couldNotPrepareTheConnection":
      "Could not prepare the connection command.",
    "ui.stacksCreatingCreation-controller.couldNotResumeTheEarlier":
      "Could not resume the earlier registration. Start a fresh attempt instead.",
    "ui.stacksCreatingCreation-controller.couldNotResumeTheOverdue":
      "Could not resume the overdue managed runtime wait.",
    "ui.stacksCreatingCreation-controller.couldNotRetryRemoteSSH":
      "Could not retry remote SSH enrollment.",
    "ui.stacksCreatingCreation-controller.couldNotStartLifecycleRetry":
      "Could not start lifecycle retry.",
    "ui.stacksCreatingCreation-controller.couldNotStartRolloutRetry":
      "Could not start rollout retry.",
    "ui.stacksCreatingCreation-controller.creatingStackKitDeployment":
      "Creating StackKit deployment",
    "ui.stacksCreatingCreation-controller.guardHasNotReportedA":
      "Guard has not reported a fresh heartbeat for this Node yet. Retry SSH enrollment on the saved connection, or verify the agent is running on the server.",
    "ui.stacksCreatingCreation-controller.jobIsNotBeingProcessed":
      "Job is not being processed",
    "ui.stacksCreatingCreation-controller.jobWasCanceled": "Job was canceled",
    "ui.stacksCreatingCreation-controller.kombifyRequestedTheAdditionalManaged":
      "kombify requested the additional managed Node. You can continue from the dashboard while enrollment finishes.",
    "ui.stacksCreatingCreation-controller.managedNodeRequestIsStill":
      "Managed Node request is still running",
    "ui.stacksCreatingCreation-controller.managedRolloutRecoveryWasNot":
      "Managed rollout recovery was not started because the waiting job has no exact lease reference.",
    "ui.stacksCreatingCreation-controller.missingJobReferencePleaseStart":
      "Missing job reference. Please start the setup wizard again.",
    "ui.stacksCreatingCreation-controller.newStackKitDeployment":
      "New StackKit deployment",
    "ui.stacksCreatingCreation-controller.noJobFound": "No job found",
    "ui.stacksCreatingCreation-controller.nodeConnected": "Node connected",
    "ui.stacksCreatingCreation-controller.nodeConnectionReady":
      "Node connection ready",
    "ui.stacksCreatingCreation-controller.nodeRegistrationDidNotReturn":
      "Node registration did not return a creation job.",
    "ui.stacksCreatingCreation-controller.nodeRegistrationReady":
      "Node registration ready",
    "ui.stacksCreatingCreation-controller.recoveringRolloutOnTheExact":
      "Recovering rollout on the exact existing managed VM...",
    "ui.stacksCreatingCreation-controller.remoteSSHEnrollmentFailed":
      "Remote SSH enrollment failed.",
    "ui.stacksCreatingCreation-controller.reportedAFreshGuardHeartbeat":
      "{name} reported a fresh Guard heartbeat and is visible in the Node projection.",
    "ui.stacksCreatingCreation-controller.restoreDrill": "Restore drill",
    "ui.stacksCreatingCreation-controller.resumingTheEarlierNodeRegistration":
      "Resuming the earlier Node registration attempt…",
    "ui.stacksCreatingCreation-controller.retryDeployment": "Retry deployment",
    "ui.stacksCreatingCreation-controller.retryRollout": "Retry rollout",
    "ui.stacksCreatingCreation-controller.retryServerRequest":
      "Retry server request",
    "ui.stacksCreatingCreation-controller.retrying": "Retrying...",
    "ui.stacksCreatingCreation-controller.retryingDeployment":
      "Retrying deployment...",
    "ui.stacksCreatingCreation-controller.retryingRolloutOnTheExisting":
      "Retrying rollout on the existing managed VM...",
    "ui.stacksCreatingCreation-controller.retryingSSHEnrollmentOnThe":
      "Retrying SSH enrollment on the saved connection…",
    "ui.stacksCreatingCreation-controller.retryingServerProvisioning":
      "Retrying server provisioning...",
    "ui.stacksCreatingCreation-controller.rolloutRecoveryDidNotReturn":
      "Rollout recovery did not return a job_id.",
    "ui.stacksCreatingCreation-controller.rolloutRetryRequiresTheExact":
      "Rollout retry requires the exact failed job and managed VM lease.",
    "ui.stacksCreatingCreation-controller.serviceVerification":
      "Service verification",
    "ui.stacksCreatingCreation-controller.sessionExpired": "Session expired",
    "ui.stacksCreatingCreation-controller.simulationGate": "Simulation gate",
    "ui.stacksCreatingCreation-controller.stackkitRollout": "StackKit rollout",
    "ui.stacksCreatingCreation-controller.stackkitRuntimeVerificationMissing":
      "StackKit runtime verification missing",
    "ui.stacksCreatingCreation-controller.startFreshAttempt":
      "Start fresh attempt",
    "ui.stacksCreatingCreation-controller.theAddNodeRequestHas":
      "The Add Node request has been running for over 2 minutes. This path should only request or prepare the additional Node, not run the full StackKit rollout. Open Operations to check whether the Node request was created, then retry Add Node if no new Node appears.",
    "ui.stacksCreatingCreation-controller.theAdditionalNodeRegistrationIs":
      "The additional Node registration is ready for the existing Homelab.",
    "ui.stacksCreatingCreation-controller.theBackendCanceledThisJob":
      "The backend canceled this job before it completed. Retry to continue from the last safe checkpoint.",
    "ui.stacksCreatingCreation-controller.theCompletedRuntimeJobDid":
      "The completed runtime job did not return verified runtime evidence. Retry the rollout to verify the installation.",
    "ui.stacksCreatingCreation-controller.theNodeProjectionCouldNot":
      "The Node projection could not be checked. The pairing command remains available, but this page will not claim a connection without a fresh Guard heartbeat.",
    "ui.stacksCreatingCreation-controller.thePreviouslyVerifiedGuardHeartbeat":
      "The previously verified Guard heartbeat is no longer fresh. Waiting for current connection evidence.",
    "ui.stacksCreatingCreation-controller.theProvisioningJobHasBeen":
      "The provisioning job has been pending for over 2 minutes without progress. This usually means the orchestrator is not running or has crashed.\n\nCheck the server logs: docker compose logs techstack\nTry restarting: docker compose restart techstack",
    "ui.stacksCreatingCreation-controller.theReservedNodeCouldNot":
      "The reserved Node could not be checked. Retry SSH enrollment on the saved connection once the server inventory is available again.",
    "ui.stacksCreatingCreation-controller.thisPageRequiresAJob":
      "This page requires a job_id from the setup wizard. Please restart setup via /stacks/new.",
    "ui.stacksCreatingCreation-controller.validatingTheSelectedStackKitAnd":
      "Validating the selected StackKit and preparing the Node...",
    "ui.stacksCreatingCreation-controller.yourRemoteNodeConfigurationIs":
      "Your remote Node configuration is ready for rollout",
    "ui.stacksCreatingCreation-controller.yourSessionIsNoLonger":
      "Your session is no longer valid, so the creation progress cannot be checked. Sign in again and reopen this creation to continue monitoring it.",
    "ui.stacksCreatingCreation-results.backendError":
      "{cleanDetails}\n\nBackend error:\n{cleanError}",
    "ui.stacksCreatingCreation-results.incidentContext": "Incident context:",
    "ui.stacksCreatingCreation-results.jobID": "Job ID: {id}",
    "ui.stacksCreatingCreation-results.runtimeDiagnostics":
      "Runtime diagnostics:",
    "ui.stacksCreatingCreation-results.stackID": "Stack ID: {stackId}",
    "ui.stacksCreatingCreation-results.targetBootstrap": "Target bootstrap:",
    "ui.stacksCreatingCreationCompletion.guardHeartbeatVerified":
      "Guard heartbeat verified",
    "ui.stacksCreatingCreationCompletion.hashPresent": "Hash present",
    "ui.stacksCreatingCreationCompletion.lastGuardHeartbeat":
      "Last Guard heartbeat:",
    "ui.stacksCreatingCreationCompletion.managedProvisioningComplete":
      "Managed provisioning complete",
    "ui.stacksCreatingCreationCompletion.materialLinked": "Material linked",
    "ui.stacksCreatingCreationCompletion.openHomelab": "Open Homelab",
    "ui.stacksCreatingCreationCompletion.owner": "Owner",
    "ui.stacksCreatingCreationCompletion.ownerPrepared": "Owner prepared",
    "ui.stacksCreatingCreationCompletion.ownerReady": "Owner ready",
    "ui.stacksCreatingCreationCompletion.ownerSeedReady": "Owner seed ready",
    "ui.stacksCreatingCreationCompletion.recovery": "Recovery",
    "ui.stacksCreatingCreationCompletion.rolloutComplete": "Rollout complete",
    "ui.stacksCreatingCreationCompletion.selfHostedRollout":
      "Self-hosted rollout",
    "ui.stacksCreatingCreationCompletion.techstackNowSeesThisNode":
      "Techstack now sees this Node as healthy and rollout-ready in the real Node projection. Pairing preparation alone did not trigger this state.",
    "ui.stacksCreatingCreationCompletion.theFirstLoginGatewayAnd":
      'The first login gateway and recovery reference arrive with the StackKit rollout ("Review + Start").',
    "ui.stacksCreatingCreationCompletion.theOwnerIdentityDerivesFrom":
      "The owner identity derives from your linked kombify Cloud profile.",
    "ui.stacksCreatingCreationCompletion.theOwnerSeedIsPrepared":
      "The owner seed is prepared.",
    "ui.stacksCreatingCreationCompletion.yourNode": "Your Node",
    "ui.stacksCreatingCreationFailure.anErrorOccurred": "An error occurred",
    "ui.stacksCreatingCreationFailure.checkYourNetworkConnection":
      "Check your network connection",
    "ui.stacksCreatingCreationFailure.copyErrorDetails": "Copy error details",
    "ui.stacksCreatingCreationFailure.earlierRegistrationAttemptAlreadyCompleted":
      "Earlier registration attempt already completed",
    "ui.stacksCreatingCreationFailure.errorDetails": "Error details",
    "ui.stacksCreatingCreationFailure.existingManagedVmLeaseReferenced":
      "Existing managed VM lease referenced",
    "ui.stacksCreatingCreationFailure.makeSureTheBackendServer":
      "Make sure the backend server is running",
    "ui.stacksCreatingCreationFailure.nodeConnectionSucceeded":
      "Node connection succeeded",
    "ui.stacksCreatingCreationFailure.operations": "Operations",
    "ui.stacksCreatingCreationFailure.possibleSolutions": "Possible solutions:",
    "ui.stacksCreatingCreationFailure.resumePreviousAttempt":
      "Resume previous attempt",
    "ui.stacksCreatingCreationFailure.reviewYourConfigurationSettings":
      "Review your configuration settings",
    "ui.stacksCreatingCreationFailure.sshEnrollmentCompletedAndThe":
      "SSH enrollment completed and the Node is visible in your homelab. Only StackKit preparation or rollout failed. Retry continues on the connected Node without repeating SSH enrollment.",
    "ui.stacksCreatingCreationFailure.theExactRetryEndpointValidates":
      "The exact retry endpoint validates this failed job and lease before continuing on the existing server.",
    "ui.stacksCreatingCreationFailure.theRolloutReachedTheExisting":
      "The rollout reached the existing managed VM.",
    "ui.stacksCreatingCreationFailure.thisBrowserReusedAnAttempt":
      "This browser reused an attempt key from a different submission. The earlier run may already have created a Node registration. Resume that attempt, or start fresh with a new key.",
    "ui.stacksCreatingCreationFailure.troubleshooting": "Troubleshooting",
    "ui.stacksCreatingCreationFailure.vpsProvisioningCompletedOnlyStackkit":
      "VPS provisioning completed. Only StackKit artifact, domain routing, or later rollout work failed.",
    "ui.stacksCreatingCreationInstallCommand.alternativeInstallationMethods":
      "Alternative installation methods",
    "ui.stacksCreatingCreationInstallCommand.copy": "Copy",
    "ui.stacksCreatingCreationInstallCommand.copyInstallationCommandToClipboard":
      "Copy installation command to clipboard",
    "ui.stacksCreatingCreationInstallCommand.installWorker": "Install worker",
    "ui.stacksCreatingCreationInstallCommand.kombifyTechstackServerUrlReachable":
      "kombify-Techstack server URL (reachable by workers):",
    "ui.stacksCreatingCreationInstallCommand.openOneHourSimulateDemo":
      "Open one-hour Simulate demo preview",
    "ui.stacksCreatingCreationInstallCommand.previewLifetimeIsLimitedTo":
      "Preview lifetime is limited to one hour.",
    "ui.stacksCreatingCreationInstallCommand.runThisCommandOnAll":
      "Run this command on all servers you want to integrate into your homelab:",
    "ui.stacksCreatingCreationInstallCommand.runThisCommandOnThe":
      "Run this command on the server or device that should become the real target for this Homelab. Simulate may also provide a one-hour demo preview of the planned StackKit.",
    "ui.stacksCreatingCreationLease.access": "Access",
    "ui.stacksCreatingCreationLease.billing": "Billing",
    "ui.stacksCreatingCreationLease.desiredState": "Desired state",
    "ui.stacksCreatingCreationLease.firstLoginAndRecovery":
      "First login and recovery",
    "ui.stacksCreatingCreationLease.host": "Host",
    "ui.stacksCreatingCreationLease.kombifyWillUseTheCaptured":
      "kombify will use the captured SSH connection details for the existing server",
    "ui.stacksCreatingCreationLease.leaseId": "Lease ID",
    "ui.stacksCreatingCreationLease.live": "Live",
    "ui.stacksCreatingCreationLease.loginGateway": "Login gateway",
    "ui.stacksCreatingCreationLease.managedNodeRequested":
      "Managed Node requested",
    "ui.stacksCreatingCreationLease.managedRuntimeReady":
      "Managed runtime ready",
    "ui.stacksCreatingCreationLease.offering": "Offering",
    "ui.stacksCreatingCreationLease.openFirstLogin": "Open first login",
    "ui.stacksCreatingCreationLease.provider": "Provider",
    "ui.stacksCreatingCreationLease.ready": "Ready",
    "ui.stacksCreatingCreationLease.remoteServerConnection":
      "Remote server connection",
    "ui.stacksCreatingCreationLease.requested": "Requested",
    "ui.stacksCreatingCreationLease.runtimeProof": "Runtime proof",
    "ui.stacksCreatingCreationLease.simulateDemoPreview":
      "Simulate demo preview",
    "ui.stacksCreatingCreationLease.stackkitDeployedOnTheLeased":
      "StackKit deployed on the leased subscription VM. Values below are returned by the runtime job - no demo data.",
    "ui.stacksCreatingCreationLease.stackkitIdentityHandoffIsMissing":
      "StackKit identity handoff is missing",
    "ui.stacksCreatingCreationLease.stackkitReturnedTheOwnerLogin":
      "StackKit returned the owner login, login gateway, and recovery references for this verified Cloud Kit rollout.",
    "ui.stacksCreatingCreationLease.status": "Status:",
    "ui.stacksCreatingCreationLease.theAdditionalSubscriptionVmRequest":
      "The additional subscription VM request is recorded. It will appear in Operations as enrollment reports back.",
    "ui.stacksCreatingCreationLease.theRolloutCompletedButThe":
      "The rollout completed, but the StackKit did not return owner login, login gateway, and recovery outputs. Treat this as a release blocker until the runtime action response includes",
    "ui.stacksCreatingCreationLease.theseStatusesComeFromRuntime":
      "These statuses come from Runtime Action responses and the final e2e proof.",
    "ui.stacksCreatingCreationLease.wallet": "Wallet",
    "ui.stacksCreatingCreationLease.yourInstallCommandIsReady":
      "Your install command is ready. kombify-simulate is used as a temporary demo preview when available and is limited to one hour.",
    "ui.stacksCreatingCreationProgress.backToSetupWizard":
      "Back to setup wizard →",
    "ui.stacksCreatingCreationProgress.creationFailed": "Creation failed",
    "ui.stacksCreatingCreationProgress.nodeProvisioningIsStillIn":
      "Node provisioning is still in progress",
    "ui.stacksCreatingCreationProgress.pleaseWaitWhileWeRe":
      "Please wait while we're",
    "ui.stacksCreatingCreationProgress.progress": "Progress",
    "ui.stacksCreatingCreationProgress.rolloutIncomplete": "Rollout incomplete",
    "ui.stacksCreatingCreationProgress.runThePairingCommand":
      "Run the pairing command",
    "ui.stacksCreatingCreationProgress.setupCannotContinue":
      "Setup cannot continue",
    "ui.stacksCreatingCreationProgress.theNextCheckIsScheduled":
      "The next check is scheduled. Dashboard, services, and Node access remain locked until enrollment is confirmed; an overdue check can be resumed safely here.",
    "ui.stacksCreatingCreationProgress.theNextManagedRuntimeCheck":
      "The next managed-runtime check is scheduled for this operation. If it remains overdue after a replica handover or restart, Techstack offers a resume action for that exact Node.",
    "ui.stacksCreatingCreationProgress.theRegistrationRequestIsReady":
      "The registration request is ready, but the Node is not connected until a fresh Guard heartbeat appears in the Node projection.",
    "ui.stacksCreatingCreationProgress.thisCanTakeAFew":
      "This can take a few minutes. Please do not close this page.",
    "ui.stacksCreatingCreationProgress.thisMayTakeAFew":
      "This may take a few minutes. You can track the progress below.",
    "ui.stacksCreatingCreationProgress.thisPageChecksTheReal":
      "This page checks the real Node projection every few seconds.",
    "ui.stacksCreatingCreationRequirements.backendRequirementsAreUnavailable":
      "Backend requirements are unavailable",
    "ui.stacksCreatingCreationRequirements.guide": "Guide →",
    "ui.stacksCreatingCreationRequirements.optional": "Optional",
    "ui.stacksCreatingCreationRequirements.required": "Required",
    "ui.stacksCreatingCreationRequirements.requiredCredentials":
      "Required credentials",
    "ui.stacksCreatingCreationRequirements.requirementsYourServersMustMeet":
      "Requirements your servers must meet",
    "ui.stacksCreatingCreationRequirements.theOrchestratorBlocksRolloutWhen":
      "The orchestrator blocks rollout when a `Required` item is missing on the target server. Optional items only emit a log warning.",
    "ui.stacksCreatingCreationRequirements.thePageIsNotFilling":
      "The page is not filling this with frontend estimates. Re-run preparation if requirements are needed for review.",
    "ui.stacksCreatingCreationRunStatus.aManualResumeIsEnabled":
      "A manual resume is enabled only after server-side validation.",
    "ui.stacksCreatingCreationRunStatus.afterCreation": "After creation",
    "ui.stacksCreatingCreationRunStatus.afterYouRunTheOne":
      "After you run the one-liner, the outbound Guard enrolls with this Homelab. The dashboard only marks the Node connected after a fresh heartbeat is present in the Node projection.",
    "ui.stacksCreatingCreationRunStatus.awaitingHeartbeat":
      "Awaiting heartbeat",
    "ui.stacksCreatingCreationRunStatus.checkSshHostCredentialsAnd":
      "Check SSH host, credentials, and that the server can reach Techstack.",
    "ui.stacksCreatingCreationRunStatus.completed": "Completed",
    "ui.stacksCreatingCreationRunStatus.connectMyNode": "Connect my Node",
    "ui.stacksCreatingCreationRunStatus.connectingToYourNodeOver":
      "Connecting to your Node over SSH…",
    "ui.stacksCreatingCreationRunStatus.connectingViaSsh":
      "Connecting via SSH…",
    "ui.stacksCreatingCreationRunStatus.continueRolloutOnTheExisting":
      "Continue rollout on the existing VM",
    "ui.stacksCreatingCreationRunStatus.copyFailedSelectTheCommand":
      "Copy failed — select the command above",
    "ui.stacksCreatingCreationRunStatus.copyPairingCommand":
      "Copy pairing command",
    "ui.stacksCreatingCreationRunStatus.currentStatus": "Current status",
    "ui.stacksCreatingCreationRunStatus.enrolling": "Enrolling",
    "ui.stacksCreatingCreationRunStatus.enrollmentOverSshFinishedThis":
      "Enrollment over SSH finished. This page will show “Node connected” once the Guard reports a fresh heartbeat.",
    "ui.stacksCreatingCreationRunStatus.failed": "Failed",
    "ui.stacksCreatingCreationRunStatus.generateAFreshCommandTo":
      "Generate a fresh command to continue connecting this Node.",
    "ui.stacksCreatingCreationRunStatus.generateAShortLivedCommand":
      "Generate a short-lived command for this Node. Connection credentials are not retained in job reports.",
    "ui.stacksCreatingCreationRunStatus.generateNewCommand":
      "Generate new command",
    "ui.stacksCreatingCreationRunStatus.generatePairingCommand":
      "Generate pairing command",
    "ui.stacksCreatingCreationRunStatus.kombifyIsInstallingAndEnrolling":
      "kombify is installing and enrolling the Guard on your server over SSH. You do not need to run a command manually.",
    "ui.stacksCreatingCreationRunStatus.kombifyWillProvisionTheSubscription":
      "kombify will provision the subscription server and then continue with the StackKit rollout.",
    "ui.stacksCreatingCreationRunStatus.kombifyWillUseTheRemote":
      "kombify will use the remote SSH configuration captured in the wizard.",
    "ui.stacksCreatingCreationRunStatus.nextScheduledCheck":
      "Next scheduled check:",
    "ui.stacksCreatingCreationRunStatus.nodeAccess": "Node access",
    "ui.stacksCreatingCreationRunStatus.pairingCommandCopied":
      "Pairing command copied",
    "ui.stacksCreatingCreationRunStatus.pairingCommandReady":
      "Pairing command ready",
    "ui.stacksCreatingCreationRunStatus.pairingCommandUnavailable":
      "Pairing command unavailable",
    "ui.stacksCreatingCreationRunStatus.plannedRemoteTarget":
      "Planned remote target",
    "ui.stacksCreatingCreationRunStatus.preparingCommand": "Preparing command…",
    "ui.stacksCreatingCreationRunStatus.provisioningInProgress":
      "Provisioning in progress",
    "ui.stacksCreatingCreationRunStatus.remoteSshConnectionFailed":
      "Remote SSH connection failed",
    "ui.stacksCreatingCreationRunStatus.remoteTarget": "Remote target",
    "ui.stacksCreatingCreationRunStatus.resumeEnrollmentOnTheExisting":
      "Resume enrollment on the existing VM",
    "ui.stacksCreatingCreationRunStatus.retryConnectionOnSavedServer":
      "Retry connection on saved server",
    "ui.stacksCreatingCreationRunStatus.retryingSshConnection":
      "Retrying SSH connection…",
    "ui.stacksCreatingCreationRunStatus.runThisOneLinerOn":
      "Run this one-liner on the additional Node. The completed registration job only created the pairing token; Techstack will show “Node connected” only after the outbound Guard reports a fresh heartbeat and the Node projection is healthy.",
    "ui.stacksCreatingCreationRunStatus.techstackUrlReachableFromThe":
      "Techstack URL reachable from the server",
    "ui.stacksCreatingCreationRunStatus.theAdditionalManagedNodeIs":
      "The additional managed Node is added to this Homelab and will appear in the Node dashboard once enrollment reports back.",
    "ui.stacksCreatingCreationRunStatus.theAdditionalNodeIsRegistered":
      "The additional Node is registered with this Homelab and can then receive services from this StackKit deployment.",
    "ui.stacksCreatingCreationRunStatus.theManagedRuntimeIsNot":
      "The Managed Runtime is not fully reachable for rollout yet. The pending signal may be its address, credentials, or enrollment. Techstack scheduled the next check.",
    "ui.stacksCreatingCreationRunStatus.theProviderOperationHasNot":
      "The provider operation has not yet handed the existing Managed Runtime over to the StackKit rollout. Techstack scheduled the next exact-lease check.",
    "ui.stacksCreatingCreationRunStatus.theScheduledCheckIsOverdue":
      "The scheduled check is overdue. Techstack validates the stack, source job, and exact lease together, then continues only the rollout on the existing VM.",
    "ui.stacksCreatingCreationRunStatus.theSshDetailsArePlanning":
      "The SSH details are planning metadata. The current Guard connection is outbound HTTPS and starts with the command below.",
    "ui.stacksCreatingCreationRunStatus.theseAccessPathsAreEnabled":
      "These access paths are enabled only after a real enrollment signal.",
    "ui.stacksCreatingCreationRunStatus.thisDoesNotCreateAnother":
      "This does not create another VM.",
    "ui.stacksCreatingCreationRunStatus.thisPairingTokenHasExpired":
      "This pairing token has expired.",
    "ui.stacksCreatingCreationRunStatus.validatingTheExistingVm":
      "Validating the existing VM...",
    "ui.stacksCreatingCreationRunStatus.waitingForGuardHeartbeat":
      "Waiting for Guard heartbeat…",
    "ui.stacksCreatingCreationRunStatus.waitingForRealConnection":
      "Waiting for real connection",
    "ui.stacksCreatingCreationRunStatus.youWillReceiveAWorker":
      "You will receive a worker installation command to connect your Nodes to this StackKit deployment.",
    "ui.stacksId.actionPending": "{action} pending",
    "ui.stacksId.actions": "Actions",
    "ui.stacksId.add": "+ Add",
    "ui.stacksId.addCredential": "Add Credential",
    "ui.stacksId.addCredentialsToEnableAuto":
      "Add credentials to enable auto-login and secure storage.",
    "ui.stacksId.backToHomelab": "← Back to Homelab",
    "ui.stacksId.configured": "Configured",
    "ui.stacksId.credentialsConfigured.one": "{count} credential configured",
    "ui.stacksId.credentialsConfigured.other": "{count} credentials configured",
    "ui.stacksId.decommission": "Decommission",
    "ui.stacksId.decommissionManagedRuntime": "Decommission managed runtime?",
    "ui.stacksId.deleteCredential": "Delete credential?",
    "ui.stacksId.deploymentFallback": "StackKit Deployment",
    "ui.stacksId.edit": "Edit",
    "ui.stacksId.editCredential": "Edit Credential",
    "ui.stacksId.enrollment": "Enrollment",
    "ui.stacksId.failedToAddCredential": "Failed to add credential",
    "ui.stacksId.failedToDeleteCredential": "Failed to delete credential",
    "ui.stacksId.failedToLoadRuntimeStatus": "Failed to load runtime status",
    "ui.stacksId.failedToLoadStackkitDeployment":
      "Failed to load StackKit deployment",
    "ui.stacksId.ingest": "Ingest",
    "ui.stacksId.latestOperationsSnapshot": "Latest operations snapshot",
    "ui.stacksId.manageCredentialsForThisStackkit":
      "Manage credentials for this StackKit deployment",
    "ui.stacksId.missingCredentialsDetected": "Missing Credentials Detected",
    "ui.stacksId.missingSecret": "Missing Secret",
    "ui.stacksId.monitoringEvidence": "Monitoring Evidence",
    "ui.stacksId.monthlyRuntime": "Monthly Runtime",
    "ui.stacksId.monthlyRuntimeActionFailed": "Monthly Runtime action failed",
    "ui.stacksId.noCredentialsConfiguredForThis":
      "No credentials configured for this StackKit deployment.",
    "ui.stacksId.noLeaseAttached": "No lease attached",
    "ui.stacksId.noStackkitDeploymentIdProvided":
      "No StackKit deployment ID provided",
    "ui.stacksId.offeringSpec": "{vcpus} vCPU · {memory} GB RAM",
    "ui.stacksId.pageTitle": "{name} - Credentials | kombify-Techstack",
    "ui.stacksId.query": "Query",
    "ui.stacksId.runtime": "Runtime",
    "ui.stacksId.series": "Series",
    "ui.stacksId.servicesDeployed.one": "{count} service deployed",
    "ui.stacksId.servicesDeployed.other": "{count} services deployed",
    "ui.stacksId.sshInfo": "SSH Info",
    "ui.stacksId.sshOff": "SSH Off",
    "ui.stacksId.sshOn": "SSH On",
    "ui.stacksId.stackkitDeploymentServices": "StackKit Deployment Services",
    "ui.stacksId.stackkitService": "StackKit service",
    "ui.stacksId.start": "Start",
    "ui.stacksId.status": "Status",
    "ui.stacksId.stop": "Stop",
    "ui.stacksId.stopManagedServer": "Stop managed server?",
    "ui.stacksId.storedCredentials": "Stored Credentials",
    "ui.stacksId.techstackWillBeginProviderCleanup":
      "Techstack will begin provider cleanup for this managed runtime.",
    "ui.stacksId.theFollowingServicesNeedCredentials":
      "The following services need credentials to be configured:",
    "ui.stacksId.theServerShutsDownAnd":
      "The server shuts down and keeps its disk, address and monthly plan. Start it again at any time.",
    "ui.stacksId.thisPermanentlyRemovesTheSelected":
      "This permanently removes the selected credential.",
    "ui.stacksId.username": "Username",
    "ui.stacksIdServersNew.addNodeKombifyTechstack":
      "Add Node | kombify-Techstack",
    "ui.stacksIdServersServerId.activityHistory": "Activity history",
    "ui.stacksIdServersServerId.addressesDomains": "Addresses & domains",
    "ui.stacksIdServersServerId.agentId": "Agent ID",
    "ui.stacksIdServersServerId.architecture": "Architecture",
    "ui.stacksIdServersServerId.backToOperations": "Back to operations",
    "ui.stacksIdServersServerId.canonicalServerAccessIsNot":
      "Canonical server access is not available yet.",
    "ui.stacksIdServersServerId.checking": "Checking...",
    "ui.stacksIdServersServerId.checks": "Checks",
    "ui.stacksIdServersServerId.confirmAction": "Confirm action",
    "ui.stacksIdServersServerId.confirmDecommission": "Confirm decommission",
    "ui.stacksIdServersServerId.confirmDetach": "Confirm detach",
    "ui.stacksIdServersServerId.couldNotLoadServerAccess":
      "Could not load server access context.",
    "ui.stacksIdServersServerId.cpuCores": "CPU cores",
    "ui.stacksIdServersServerId.custodyResolutionFailed":
      "Custody resolution failed.",
    "ui.stacksIdServersServerId.decommissionFailed": "Decommission failed.",
    "ui.stacksIdServersServerId.decommissionServer": "Decommission server",
    "ui.stacksIdServersServerId.desiredListenersDurableReservationsAnd":
      "Desired listeners, durable reservations, and runtime evidence for this Node.",
    "ui.stacksIdServersServerId.detachSelfOwnedServer":
      "Detach self-owned server",
    "ui.stacksIdServersServerId.detachServer": "Detach server",
    "ui.stacksIdServersServerId.detaching": "Detaching...",
    "ui.stacksIdServersServerId.disk": "Disk",
    "ui.stacksIdServersServerId.drift": "Drift",
    "ui.stacksIdServersServerId.exposed": "Exposed",
    "ui.stacksIdServersServerId.intent": "Intent",
    "ui.stacksIdServersServerId.lastSeen": "Last seen",
    "ui.stacksIdServersServerId.lifecycleActionsBecomeAvailableWhen":
      "Lifecycle actions become available when this server is connected, approved, and assigned to the stack.",
    "ui.stacksIdServersServerId.listener": "Listener",
    "ui.stacksIdServersServerId.liveStreamReconnecting":
      "Live stream reconnecting…",
    "ui.stacksIdServersServerId.loadingServerDetails": "Loading server details",
    "ui.stacksIdServersServerId.logs": "Logs",
    "ui.stacksIdServersServerId.manageSettingsThatAffectThis":
      "Manage settings that affect this server's lifecycle. Routine status and service controls remain in their respective tabs.",
    "ui.stacksIdServersServerId.managedRuntimeLeaseContext":
      "Managed runtime lease context.",
    "ui.stacksIdServersServerId.memory": "Memory",
    "ui.stacksIdServersServerId.metadata": "Metadata",
    "ui.stacksIdServersServerId.noCompilerDeclaredReservationOr":
      "No compiler-declared reservation or bound runtime listener was present in the latest complete Guard snapshot.",
    "ui.stacksIdServersServerId.noDesiredListenerIsReserved":
      "No desired listener is reserved, and the Guard could not complete its listener snapshot, so runtime listeners remain unknown.",
    "ui.stacksIdServersServerId.noDesiredListenerIsReserved2":
      "No desired listener is reserved, and the Guard has not reported a listener snapshot for this Node yet.",
    "ui.stacksIdServersServerId.noHostAddressHasBeen":
      "No host address has been reported.",
    "ui.stacksIdServersServerId.noObservedServiceEndpointsHave":
      "No observed service endpoints have been reported by this server.",
    "ui.stacksIdServersServerId.noPortAllocationsRecorded":
      "No port allocations recorded",
    "ui.stacksIdServersServerId.noPortEvidenceYet": "No port evidence yet",
    "ui.stacksIdServersServerId.noPreCheckResultIs":
      "No pre-check result is recorded yet.",
    "ui.stacksIdServersServerId.noServerInstallerOrStackkits":
      "No server, installer, or StackKits logs are recorded yet.",
    "ui.stacksIdServersServerId.noServiceDomainsReported":
      "No service domains reported.",
    "ui.stacksIdServersServerId.noServicePlacementIsRecorded":
      "No service placement is recorded for this server.",
    "ui.stacksIdServersServerId.noStackkitDeploymentEvidenceHas":
      "No StackKit deployment evidence has been reported by this server.",
    "ui.stacksIdServersServerId.notDeclared": "Not declared",
    "ui.stacksIdServersServerId.operatingSystem": "Operating system",
    "ui.stacksIdServersServerId.portAllocations": "Port allocations",
    "ui.stacksIdServersServerId.portEvidenceIsPartial":
      "Port evidence is partial",
    "ui.stacksIdServersServerId.portInventoryIsNotAvailable":
      "Port inventory is not available yet",
    "ui.stacksIdServersServerId.preChecks": "Pre-checks",
    "ui.stacksIdServersServerId.reconnectFailed": "Reconnect failed.",
    "ui.stacksIdServersServerId.reservation": "Reservation",
    "ui.stacksIdServersServerId.resolveStaleCustodyRecord":
      "Resolve stale custody record",
    "ui.stacksIdServersServerId.resolveStaleRecord": "Resolve stale record",
    "ui.stacksIdServersServerId.runtimeEvidenceIsPartialUnknown":
      "Runtime evidence is partial. Unknown states stay unknown until the Guard reports a complete listener or exposure snapshot.",
    "ui.stacksIdServersServerId.serverCleanupInProgress":
      "Server cleanup in progress",
    "ui.stacksIdServersServerId.serverDetachFailed": "Server detach failed.",
    "ui.stacksIdServersServerId.serverDetailSections": "Server detail sections",
    "ui.stacksIdServersServerId.serverDetailsKombifyTechstack":
      "Server Details | kombify-Techstack",
    "ui.stacksIdServersServerId.serverGenerationDecommissioned":
      "Server generation decommissioned",
    "ui.stacksIdServersServerId.serverSettings": "Server settings",
    "ui.stacksIdServersServerId.serviceEndpoints": "Service endpoints",
    "ui.stacksIdServersServerId.servicesOnThisServerMay":
      "Services on this server may become unavailable. The request cannot be treated as complete until provider absence has been verified.",
    "ui.stacksIdServersServerId.stackkitActions": "StackKit actions",
    "ui.stacksIdServersServerId.theAgentIdentityWillBe":
      "The Agent identity will be revoked immediately. The physical server and its provider account remain untouched.",
    "ui.stacksIdServersServerId.theOldProviderGenerationCannot":
      "The old provider generation cannot be started again. Use Recreate above after exact provider absence and capacity release have been verified.",
    "ui.stacksIdServersServerId.thisChangesTheSelectedServer":
      "This changes the selected server through its enrolled StackKit agent.",
    "ui.stacksIdServersServerId.thisIsConfiguredStackkitIntent":
      "This is configured StackKit intent; the Guard has not reported matching deployment evidence yet.",
    "ui.stacksIdServersServerId.thisOnlyArchivesTheStale":
      "This only archives the stale Techstack custody record. It does not delete anything at the provider.",
    "ui.stacksIdServersServerId.thisServerHasNoManaged":
      "This server has no managed provider lease. Its recorded custody does not currently authorize either managed decommission or self-owned detach.",
    "ui.stacksIdServersServerId.thisServerIsALegacy":
      "This server is a legacy or unbound custody record. Confirm that the provider resource has already been removed; Techstack will archive only this record and will not call or delete a provider resource.",
    "ui.stacksIdServersServerId.thisServerIsRegisteredThrough":
      "This server is registered through the worker inventory; managed runtime access actions are not attached.",
    "ui.standardBundle.additionalComputeNodeForStackKit":
      "Additional compute Node for StackKit service placement.",
    "ui.standardBundle.additionalNodeIntendedForStorage":
      "Additional Node intended for storage-heavy services.",
    "ui.standardBundle.addsALoginProtectedPassword":
      "Adds a login-protected password vault when the Vault use case is selected.",
    "ui.standardBundle.authAndDatabaseBackend": "Auth and database backend",
    "ui.standardBundle.basementKitStandardRelease":
      "Basement Kit standard release",
    "ui.standardBundle.collectsTelemetrySignalsForThe":
      "Collects telemetry signals for the Day-2 monitoring baseline.",
    "ui.standardBundle.createsASecureMeshNetwork":
      "Creates a secure mesh network for device access without public ports.",
    "ui.standardBundle.defaultIdentityProviderForLogin":
      "Default identity provider for login-protected services",
    "ui.standardBundle.directPrivateMesh": "Direct private mesh",
    "ui.standardBundle.expandsToTheImmichServer":
      "Expands to the Immich server, machine-learning worker, Postgres, and Redis specs.",
    "ui.standardBundle.fileStorageModule": "File storage module",
    "ui.standardBundle.files": "Files",
    "ui.standardBundle.firstCoreNodeWireCompatible":
      "First/core Node; wire-compatible with main, standalone, and control-plane.",
    "ui.standardBundle.foundationNode": "Foundation Node",
    "ui.standardBundle.germanyEUPrimary": "Germany / EU primary",
    "ui.standardBundle.germanyEUSecondary": "Germany / EU secondary",
    "ui.standardBundle.goals": "Goals",
    "ui.standardBundle.handlesRoutingHTTPSAndService":
      "Handles routing, HTTPS, and service entrypoints for the stack.",
    "ui.standardBundle.localNetworkOnly": "Local network only",
    "ui.standardBundle.localOrUserOwnedNode":
      "Local or user-owned Node target for first rollout and expansion.",
    "ui.standardBundle.login": "Login",
    "ui.standardBundle.managedVPSFoundationForKombify":
      "Managed VPS foundation for kombify Cloud rollouts.",
    "ui.standardBundle.none": "None",
    "ui.standardBundle.optionalBackendCapabilityWhenA":
      "Optional backend capability when a stack needs PocketBase-native auth or data.",
    "ui.standardBundle.passwordVault": "Password vault",
    "ui.standardBundle.photoLibraryWithSupportingDatabase":
      "Photo library with supporting database, cache, and ML services",
    "ui.standardBundle.pocketIDIsTheStandard":
      "Pocket ID is the standard external identity head for StackKit access.",
    "ui.standardBundle.reservedForTheFileStorage":
      "Reserved for the file-storage module once it is in the release baseline.",
    "ui.standardBundle.reverseProxyAndLoadBalancer":
      "Reverse proxy and load balancer",
    "ui.standardBundle.standardObservabilityPipeline":
      "Standard observability pipeline",
    "ui.standardBundle.storageNode": "Storage Node",
    "ui.standardBundle.tailscaleCompatibleMeshVPN":
      "Tailscale-compatible mesh VPN",
    "ui.standardBundle.users": "Users",
    "ui.standardBundle.workerNode": "Worker Node",
    "ui.strata.aRolloutIsInProgress":
      "A rollout is in progress on {rolloutNode}.",
    "ui.strata.activeNote": "active",
    "ui.strata.alertsActive.one": "{count} alert is active.",
    "ui.strata.alertsActive.other": "{count} alerts are active.",
    "ui.strata.backup": "Backup",
    "ui.strata.centre": "Centre · {apps} apps, {system} system",
    "ui.strata.cpuCurve": "{name} CPU over the last 24 hours",
    "ui.strata.gaugeAria":
      "{name}: CPU {cpu} percent, memory {ram} percent, disk {disk} percent, {services} services",
    "ui.strata.job": "Job",
    "ui.strata.noCpuCurve": "{name}: no CPU data for the last 24 hours",
    "ui.strata.noNodeHasReportedYet": "No Node has reported yet.",
    "ui.strata.nodesNeedAttention.one":
      "{count} of {total} Nodes needs attention.",
    "ui.strata.nodesNeedAttention.other":
      "{count} of {total} Nodes need attention.",
    "ui.strata.notShown": "Not shown: {items} could not be read.",
    "ui.strata.provisioning": "Provisioning",
    "ui.strata.quiet": "quiet",
    "ui.strata.removal": "Removal",
    "ui.strata.restore": "Restore",
    "ui.strata.rightNow": "right now",
    "ui.strata.rollout": "Rollout",
    "ui.strata.theLastRolloutOnFailed":
      "The last rollout on {failureNode} failed.",
    "ui.strata.upgrade": "Upgrade",
    "ui.strataDashboard.alerts": "Alerts",
    "ui.strataDashboard.cpuSeriesUnavailable": "CPU series unavailable",
    "ui.strataDashboard.devices": "Devices",
    "ui.strataDashboard.greenBelow75AmberFrom":
      "Green below 75 %, amber from 75 %, red from 90 %.",
    "ui.strataDashboard.innerRing": "Inner ring",
    "ui.strataDashboard.loadingCpu": "Loading CPU…",
    "ui.strataDashboard.middleRing": "Middle ring",
    "ui.strataDashboard.monitoring": "Monitoring →",
    "ui.strataDashboard.noCpuDataInThe": "No CPU data in the last 24 h",
    "ui.strataDashboard.nodes": "Nodes",
    "ui.strataDashboard.nothingHappenedNoRolloutsRestores":
      "Nothing happened: no rollouts, restores, alerts or outages.",
    "ui.strataDashboard.outerRing": "Outer ring",
    "ui.substrate.expires":
      "Expires: {time}. The command is shown only here and is not retained in job reports.",
    "ui.substrateEnrollment.aConnectionCommandCouldNot":
      "A connection command could not be prepared.",
    "ui.substrateEnrollment.couldNotPrepareTheConnection":
      "Could not prepare the connection.",
    "ui.substrateEnrollment.generateANewCommand": "Generate a new command",
    "ui.substrateEnrollment.generateConnectionCommand":
      "Generate connection command",
    "ui.substrateEnrollment.preparing": "Preparing...",
    "ui.substrateEnrollment.proxmoxHypervisorConnection":
      "Proxmox hypervisor connection",
    "ui.substrateEnrollment.theHypervisorHostsYourNodes":
      "The hypervisor hosts your Nodes. StackKits run inside Ubuntu guests. Guest management is enabled after its local Proxmox client is configured and you authorize the connection. Existing guests stay outside deletion custody.",
    "ui.substrates.theRuntimeDidNotReturn":
      "The runtime did not return the requested data.",
    "ui.taskStatus.aVerifiedStackKitDeploymentMust":
      "A verified StackKit deployment must have a tested recovery path for the default services.",
    "ui.taskStatus.analyzingYourGoalsToFind":
      "Analyzing your goals to find the best-fitting StackKit template.",
    "ui.taskStatus.applyingSecurityPolicies": "Applying security policies",
    "ui.taskStatus.basedOnYourGoalsThe":
      "Based on your goals, the Unifier selects containers, sets resource limits, and resolves dependencies between services.",
    "ui.taskStatus.buildingServiceList": "Building service list",
    "ui.taskStatus.callingTheStackKitsRuntimeAction":
      "Calling the StackKits runtime action that applies the generated Cloud Kit specification.",
    "ui.taskStatus.checkingOpenTofu": "Checking OpenTofu",
    "ui.taskStatus.checkingRolloutTarget": "Checking rollout target",
    "ui.taskStatus.checkingTerramate": "Checking Terramate",
    "ui.taskStatus.checkingThatLoginProtectedServices":
      "Checking that login-protected services and monitoring signals are available after rollout.",
    "ui.taskStatus.checkingTheTerramateToolchainWhen":
      "Checking the Terramate toolchain when the selected StackKit lifecycle needs it.",
    "ui.taskStatus.checkingYourChoices": "Checking your choices",
    "ui.taskStatus.collectingServiceMetadataExposedBy":
      "Collecting service metadata exposed by the StackKits rollout.",
    "ui.taskStatus.combiningUserIntentStackKitDefaults":
      "Combining user intent, StackKit defaults, and runtime information into the final deployment spec.",
    "ui.taskStatus.compilingEverythingIntoAFinal":
      "Compiling everything into a final StackKits deployment specification.",
    "ui.taskStatus.configureStackKitDeployment":
      "Configure StackKit deployment",
    "ui.taskStatus.configuringAccessProfilesReverseProxy":
      "Configuring access profiles, reverse proxy, DNS, and internal networking.",
    "ui.taskStatus.configuringAuthentication": "Configuring authentication",
    "ui.taskStatus.configuringFirewallRulesTLSCertificates":
      "Configuring firewall rules, TLS certificates, and isolation settings.",
    "ui.taskStatus.configuringNetworkSettings": "Configuring network settings",
    "ui.taskStatus.configuringSingleSignOnUser":
      "Configuring single sign-on, user accounts, and access control.",
    "ui.taskStatus.confirmingServicesAndValidatingThe":
      "Confirming services and validating the restore drill",
    "ui.taskStatus.confirmingThatTheBoundManaged":
      "Confirming that the bound managed VPS can be addressed by the StackKits CLI.",
    "ui.taskStatus.confirmingVPSTarget": "Confirming VPS target",
    "ui.taskStatus.connectingToRuntime": "Connecting to runtime",
    "ui.taskStatus.creatingOrBindingTheSubscription":
      "Creating or binding the subscription VM lease for this StackKit rollout.",
    "ui.taskStatus.creatingYourDeploymentSpec": "Creating your deployment spec",
    "ui.taskStatus.determiningWhichServicesAreNeeded":
      "Determining which services are needed and applying best-practice defaults.",
    "ui.taskStatus.eachServiceGetsScopedPermissions":
      "Each service gets scoped permissions. TLS is enabled automatically where possible, and services are isolated by default.",
    "ui.taskStatus.ensuringTheDeploymentTargetSatisfies":
      "Ensuring the deployment target satisfies the StackKit runtime requirements.",
    "ui.taskStatus.findingTheBestStackKitFor":
      "Finding the best StackKit for you",
    "ui.taskStatus.generateDeploymentArtifacts":
      "Generate deployment artifacts",
    "ui.taskStatus.generatingDeploymentSpec": "Generating deployment spec",
    "ui.taskStatus.generatingStackKitIaC": "Generating StackKit IaC",
    "ui.taskStatus.generatingUnifiedSpec": "Generating unified spec",
    "ui.taskStatus.identifyingServicesBestPractices":
      "Identifying services & best practices",
    "ui.taskStatus.ifAptOrUnattendedUpgrades":
      "If apt or unattended upgrades block package installation, Techstack keeps the bound VM visible and shows the collected diagnostics.",
    "ui.taskStatus.installingAndCheckingDocker":
      "Installing and checking Docker",
    "ui.taskStatus.installingOrValidatingTheDocker":
      "Installing or validating the Docker runtime used by the selected services.",
    "ui.taskStatus.loadingThePersistedIntentAnd":
      "Loading the persisted intent and waiting for the managed VPS target to become reachable.",
    "ui.taskStatus.managedKombifyCloudRolloutsUse":
      "Managed kombify Cloud rollouts use the VM lease target; user-owned rollout targets require approved workers.",
    "ui.taskStatus.matchingAStackKit": "Matching a StackKit",
    "ui.taskStatus.networkSettingsAreDerivedFrom":
      "Network settings are derived from your access mode. Local-only uses internal Docker networking; remote access adds the lane-appropriate private mesh or managed edge route.",
    "ui.taskStatus.onceThisSucceedsTheNode":
      "Once this succeeds, the Node projection is kept visible in Techstack even if preparation or rollout fails later.",
    "ui.taskStatus.opentofuReadinessIsPartOf":
      "OpenTofu readiness is part of the StackKits CLI prep contract, not a separate Techstack-owned bootstrap path.",
    "ui.taskStatus.persistingConfiguration": "Persisting configuration",
    "ui.taskStatus.persistingRolloutSpec": "Persisting rollout spec",
    "ui.taskStatus.preparingDocker": "Preparing Docker",
    "ui.taskStatus.preparingNodeRegistration": "Preparing Node registration",
    "ui.taskStatus.preparingOpenTelemetryHandoffDataFor":
      "Preparing OpenTelemetry handoff data for monitoring and operations.",
    "ui.taskStatus.preparingStackKitsRuntime": "Preparing StackKits runtime",
    "ui.taskStatus.preparingTelemetry": "Preparing telemetry",
    "ui.taskStatus.preparingTheRuntimeMetadataUsed":
      "Preparing the runtime metadata used by monitoring, operations, and the Runtime Intelligence Layer.",
    "ui.taskStatus.preparingToolsApplyingTheStackKit":
      "Preparing tools, applying the StackKit, and reading services",
    "ui.taskStatus.provisionManagedRuntime": "Provision managed runtime",
    "ui.taskStatus.readingServiceInventory": "Reading service inventory",
    "ui.taskStatus.renderingStackKitsArtifactsAfterVPS":
      "Rendering StackKits artifacts after VPS readiness",
    "ui.taskStatus.renderingTheStackKitInfrastructureFiles":
      "Rendering the StackKit infrastructure files needed by the rollout adapter.",
    "ui.taskStatus.requestingManagedCloudNode": "Requesting managed cloud Node",
    "ui.taskStatus.requestingManagedNode": "Requesting managed Node",
    "ui.taskStatus.reservingTheSubscriptionVMConnecting":
      "Reserving the subscription VM, connecting runtime, and preparing telemetry",
    "ui.taskStatus.rollOutCloudKit": "Roll out Cloud Kit",
    "ui.taskStatus.rollingOutCloudKit": "Rolling out Cloud Kit",
    "ui.taskStatus.runningRestoreDrill": "Running restore drill",
    "ui.taskStatus.runningSimulatedUpdateGate": "Running simulated update gate",
    "ui.taskStatus.runningSimulationGate": "Running simulation gate",
    "ui.taskStatus.runningTheStackKitsCLIPrepare":
      "Running the StackKits CLI prepare contract on the managed VPS.",
    "ui.taskStatus.savingUnifiedSpecYamlSo":
      "Saving unified-spec.yaml so the rollout is reproducible and auditable.",
    "ui.taskStatus.savingYourChoicesToThe":
      "Saving your choices to the database so they can be referenced during deployment.",
    "ui.taskStatus.savingYourConfiguration": "Saving your configuration",
    "ui.taskStatus.settingUpAuthentication": "Setting up authentication",
    "ui.taskStatus.settingUpNetworking": "Setting up networking",
    "ui.taskStatus.settingUpSecurityConfiguration":
      "Setting up security configuration",
    "ui.taskStatus.stackkitsAreCuratedInfrastructureTemplates":
      "StackKits are curated infrastructure templates. The system picks one that covers your selected features with minimal overhead.",
    "ui.taskStatus.startingTelemetryHandoff": "Starting telemetry handoff",
    "ui.taskStatus.techstackConsumesStackKitArtifactsHere":
      "Techstack consumes StackKit artifacts here; StackKits remains responsible for applying them.",
    "ui.taskStatus.techstackRecordsTheManagedTarget":
      "Techstack records the managed target and prepares the orchestration handoff before StackKits performs the Cloud Kit rollout.",
    "ui.taskStatus.terramateReadinessBelongsToStackKits":
      "Terramate readiness belongs to StackKits lifecycle preparation; Techstack does not require it for the initial managed VPS lease.",
    "ui.taskStatus.theDashboardCanShowManaged":
      "The dashboard can show managed services as soon as StackKits exposes them, while later verification continues.",
    "ui.taskStatus.theFirstRolloutRecordsThe":
      "The first rollout records the runtime context that later service cards, metrics, and RIL workflows consume.",
    "ui.taskStatus.theLeaseCapturesRuntimeState":
      "The lease captures runtime state, billing cadence, and the managed provider that will host the Cloud Kit.",
    "ui.taskStatus.thePersistedSpecLinksBack":
      "The persisted spec links back to the requirements file and the original StackKit deployment request.",
    "ui.taskStatus.theSimulationGateProtectsThe":
      "The simulation gate protects the first rollout and later update flows from unsafe changes.",
    "ui.taskStatus.theSpecContainsAllConfiguration":
      "The spec contains all configuration needed to deploy your Homelab. It can be version-controlled and reproduced on any compatible Node.",
    "ui.taskStatus.theUnifiedSpecIsThe":
      "The unified spec is the canonical input for StackKits and runtime verification.",
    "ui.taskStatus.thisChecksFeatureSelectionsAccess":
      "This checks feature selections, access modes, user configuration, and authentication settings for consistency.",
    "ui.taskStatus.thisChecksThatThePersisted":
      "This checks that the persisted StackKit deployment spec and requirements-spec.yaml still match, then confirms the managed VM lease exposes a runtime SSH host or public IP before StackKits artifact generation starts.",
    "ui.taskStatus.thisIsThePointWhere":
      "This is the point where the selected services are installed and configured on the runtime target.",
    "ui.taskStatus.thisNonInteractivePrepStep":
      "This non-interactive prep step installs and checks the tools StackKits needs before applying the Cloud Kit.",
    "ui.taskStatus.thisStackKitDeploymentConfigurationIs":
      "This StackKit deployment configuration is stored securely and can be exported or modified later from the dashboard.",
    "ui.taskStatus.validatingChoicesAndBuildingThe":
      "Validating choices and building the deployment spec",
    "ui.taskStatus.validatingConfiguration": "Validating configuration",
    "ui.taskStatus.validatingTheBackupAndRestore":
      "Validating the backup and restore path before the StackKit deployment is marked verified.",
    "ui.taskStatus.validatingTheUpdatePathBefore":
      "Validating the update path before applying the rollout to the managed runtime.",
    "ui.taskStatus.verificationConfirmsThatTheStackKit":
      "Verification confirms that the StackKit deployment is usable, not only that files were generated.",
    "ui.taskStatus.verifyRollout": "Verify rollout",
    "ui.taskStatus.verifyingLoginProtectedServices":
      "Verifying login-protected services",
    "ui.taskStatus.verifyingServices": "Verifying services",
    "ui.taskStatus.verifyingThatAllSelectedOptions":
      "Verifying that all selected options are valid and compatible with each other.",
    "ui.taskStatus.verifyingTheInfrastructureToolchainNeeded":
      "Verifying the infrastructure toolchain needed by StackKits.",
    "ui.taskStatus.yourChosenAuthMethodIs":
      "Your chosen auth method is applied across all services. Multi-user setups get group-based permissions automatically.",
    "ui.taskUpdates.anUnexpectedErrorOccurred": "An unexpected error occurred",
    "ui.techstackBrandLogo.kombifyTechstack": "kombify Techstack",
    "ui.terminal.title": "Terminal · {name}",
    "ui.time.daysHours": "{days} d {hours} h",
    "ui.time.hours": "{count} h",
    "ui.time.hoursAgo": "{count} h ago",
    "ui.time.hoursMinutes": "{hours} h {minutes}",
    "ui.time.justNow": "just now",
    "ui.time.minutes": "{count} min",
    "ui.time.minutesAgo": "{count} min ago",
    "ui.time.minutesShort": "{count} m",
    "ui.time.notReported": "not reported",
    "ui.time.seconds": "{count} s",
    "ui.types.mainController": "Main Controller",
    "ui.types.probing": "Probing...",
    "ui.types.routerGateway": "Router/Gateway",
    "ui.types.scanning": "Scanning...",
    "ui.types.skipped": "Skipped",
    "ui.types.unknown": "Unknown",
    "ui.types.utility": "Utility",
    "ui.userMenu.version": "Version {version}",
    "ui.wallet.accessControl": "Access control",
    "ui.wallet.accessControls": "Access Controls",
    "ui.wallet.accessEntry": "Access Entry",
    "ui.wallet.addAccess": "+ Add Access",
    "ui.wallet.addBreakGlassCredentialsFallback":
      "Add break-glass credentials, fallback material, or reveal-only secrets here.",
    "ui.wallet.addItem": "Add {label}",
    "ui.wallet.addTool": "+ Add Tool",
    "ui.wallet.allTypes": "All Types",
    "ui.wallet.apiKeys": "API Keys",
    "ui.wallet.applicationAdministratorWithFullHomelab":
      "Application administrator with full Homelab management",
    "ui.wallet.auto": "Auto",
    "ui.wallet.autoGenerated": "Auto-generated",
    "ui.wallet.breakGlassMaterialCredentialsAnd":
      "Break-glass material, credentials, and reveal-only secrets stay in the recovery zone.",
    "ui.wallet.breakGlassMaterialRevealOnly":
      "Break-glass material, reveal-only credentials, and fallback secrets will appear here.",
    "ui.wallet.browserExtensionTriggeredCheckFor":
      "Browser extension triggered! Check for save prompt.",
    "ui.wallet.certificates": "Certificates",
    "ui.wallet.clearFilters": "Clear Filters",
    "ui.wallet.clearWalletSearch": "Clear wallet search",
    "ui.wallet.collectionEditing": "Collection editing",
    "ui.wallet.confirmCredentialReveal": "Confirm credential reveal",
    "ui.wallet.considerRotating": "{names} — consider rotating immediately.",
    "ui.wallet.context": "Context",
    "ui.wallet.copyRoleEmail": "Copy {role} email",
    "ui.wallet.copyRoleSecret": "Copy {role} secret",
    "ui.wallet.couldNotTriggerBrowserExtension":
      "Could not trigger browser extension.",
    "ui.wallet.currentPassword": "Current password",
    "ui.wallet.databaseManagement": "Database management",
    "ui.wallet.daysLeft": "{days}d left",
    "ui.wallet.daysShort": "{days}d",
    "ui.wallet.deleteWalletEntry": "Delete wallet entry?",
    "ui.wallet.developerAccessForTestingAnd":
      "Developer access for testing and development",
    "ui.wallet.discover": "Discover",
    "ui.wallet.discoverServiceCredentials": "Discover service credentials",
    "ui.wallet.dismiss": "Dismiss",
    "ui.wallet.editItem": "Edit {label}",
    "ui.wallet.enterNewSecretValue": "Enter new secret value",
    "ui.wallet.enterYourCurrentLocalTechstack":
      "Enter your current local Techstack password to reveal this wallet entry.",
    "ui.wallet.expired": "Expired",
    "ui.wallet.expiredCredentials.one": "{count} Expired Credential",
    "ui.wallet.expiredCredentials.other": "{count} Expired Credentials",
    "ui.wallet.expires": "Expires",
    "ui.wallet.expiringCredentials.one": "{count} Credential Expiring Soon",
    "ui.wallet.expiringCredentials.other": "{count} Credentials Expiring Soon",
    "ui.wallet.extensionTriggered": "Extension Triggered!",
    "ui.wallet.freshKombifyCloudReAuthentication":
      "Fresh kombify Cloud re-authentication is required before wallet material can be revealed.",
    "ui.wallet.generateSshKey": "Generate SSH Key",
    "ui.wallet.hide": "Hide",
    "ui.wallet.hideRoleSecret": "Hide {role} secret",
    "ui.wallet.homelabManagement": "Homelab management",
    "ui.wallet.humanUsersAdminIdentitiesAnd":
      "Human users, admin identities, and system-owned login surfaces live here as the operational access layer.",
    "ui.wallet.identity": "Identity",
    "ui.wallet.importExport": "Import / Export",
    "ui.wallet.kombifyTechstackAdmin": "kombify-Techstack Admin",
    "ui.wallet.kombifyTechstackDeveloper": "kombify-Techstack Developer",
    "ui.wallet.launchReadyToolsAndUrl":
      "Launch-ready tools and URL-backed service entries will surface here as Wallet becomes the central starting point.",
    "ui.wallet.localBackendAdmin": "Local backend admin",
    "ui.wallet.localBackendSuperuser": "Local backend superuser",
    "ui.wallet.logViewing": "Log viewing",
    "ui.wallet.manage": "Manage",
    "ui.wallet.managedUser": "Managed user",
    "ui.wallet.managedUsers": "Managed Users",
    "ui.wallet.newSecret": "New Secret",
    "ui.wallet.noMatchingRecoveryItems": "No matching recovery items",
    "ui.wallet.noRecoveryItemsInThis": "No recovery items in this view",
    "ui.wallet.noRecoveryItemsYet": "No recovery items yet",
    "ui.wallet.noToolsYet": "No tools yet",
    "ui.wallet.notSet": "Not set",
    "ui.wallet.oauthTokens": "OAuth Tokens",
    "ui.wallet.other": "Other",
    "ui.wallet.passwords": "Passwords",
    "ui.wallet.quickSync": "Quick-Sync",
    "ui.wallet.quickSyncFailedTryCopying":
      "Quick-Sync failed. Try copying manually.",
    "ui.wallet.quickSyncToPasswordManager": "Quick-Sync to Password Manager",
    "ui.wallet.recovery": "+ Recovery",
    "ui.wallet.recoveryItem": "Recovery Item",
    "ui.wallet.recoveryItems": "Recovery Items",
    "ui.wallet.reset": "Reset",
    "ui.wallet.reveal": "Reveal",
    "ui.wallet.revealRoleSecret": "Reveal {role} secret",
    "ui.wallet.rotateCredential": "Rotate Credential",
    "ui.wallet.rotating": "Rotating:",
    "ui.wallet.rotating2": "Rotating...",
    "ui.wallet.save": "Save",
    "ui.wallet.saveToExternalPasswordManager":
      "Save to external password manager (1Password, Bitwarden, etc.)",
    "ui.wallet.searchCredentials": "Search credentials...",
    "ui.wallet.secret": "Secret",
    "ui.wallet.selfHostedCompatibilityAdminAccess":
      "Self-hosted compatibility admin access",
    "ui.wallet.show": "Show",
    "ui.wallet.simulationAccess": "Simulation access",
    "ui.wallet.sshKeys": "SSH Keys",
    "ui.wallet.storedCount": "{count} stored",
    "ui.wallet.syncing": "Syncing...",
    "ui.wallet.systemSettings": "System settings",
    "ui.wallet.systemUsers": "System Users",
    "ui.wallet.thisPermanentlyRemovesTheSelected":
      "This permanently removes the selected wallet entry.",
    "ui.wallet.thisWillUpdateTheSecret":
      "This will update the secret and record the rotation date for tracking.",
    "ui.wallet.tool": "Tool",
    "ui.wallet.toolSurface": "Tool surface",
    "ui.wallet.tools": "Tools",
    "ui.wallet.triggering": "Triggering...",
    "ui.wallet.tryAdjustingYourSearchQuery":
      "Try adjusting your search query or filter for reveal-only entries.",
    "ui.wallet.user": "User",
    "ui.wallet.userManagement": "User management",
    "ui.wallet.walletSections": "Wallet sections",
    "ui.wallet.walletShouldBeTheFirst":
      "Wallet should be the first place to launch tools, services, and operational surfaces without dropping into reveal flows.",
    "ui.wallet.walletStaysSplitIntoLaunch":
      "Wallet stays split into launch surfaces, operational access, and recovery-only material.",
    "ui.wallet.youDonTHavePermission":
      "You don't have permission to add wallet entries.",
    "ui.wallet.youDonTHavePermission2":
      "You don't have permission to delete this credential.",
    "ui.windowsOnboarding.serverConnection": "Server connection",
    "ui.wizardPreviewController.recommendationPreviewIsTemporarilyUnavailable":
      "Recommendation preview is temporarily unavailable.",
    "ui.wizardRunRequest.selectAConnectedHypervisorAnd":
      "Select a connected hypervisor and valid Ubuntu guest resources.",
    "ui.wizardRunRequest.selectSeparateHomeAssistantOS":
      "Select separate Home Assistant OS guest resources.",
    "ui.wizardRuns.thisRunHasNoRollout": "This run has no rollout to cancel.",
    "ui.worker.registryLink": "Worker-Registry Link",
    "ui.worker.registryUrlError":
      "Could not automatically determine registry URL: {error}",
    "ui.worker.runInstallMany":
      "Run the install command above on at least {count} Nodes, then wait for operations evidence to confirm the connection.",
    "ui.worker.runInstallOne":
      "Run the install command above on your Node, then wait for operations evidence to confirm the connection.",
    "ui.worker.verifiedConnected": "{count} verified connected",
    "ui.worker.waitingVerified":
      "Waiting for verified workers ({connected}/{required})",
    "ui.workerRegistrationCard.installCommand": "Install command:",
    "ui.workerRegistrationCard.installCommandForNewWorkers":
      "Install command for new workers:",
    "ui.workerRegistrationCard.nextStep": "Next step:",
    "ui.workerRegistrationCard.operationsEvidenceIsRequiredBefore":
      "Operations evidence is required before connected Nodes can be confirmed.",
    "ui.workerRegistrationCard.workersConnectedVerified":
      "Workers connected (verified)",
  },
  de: {
    "state.active": "aktiv",
    "state.adopting": "wird übernommen",
    "state.archived": "archiviert",
    "state.awaiting_pairing": "wartet auf Pairing",
    "state.canceled": "abgebrochen",
    "state.cancelled": "abgebrochen",
    "state.completed": "abgeschlossen",
    "state.configured": "konfiguriert",
    "state.connected": "verbunden",
    "state.connecting": "verbindet",
    "state.consistent": "konsistent",
    "state.decommissioned": "außer Betrieb genommen",
    "state.decommissioning": "wird außer Betrieb genommen",
    "state.degraded": "beeinträchtigt",
    "state.deploying": "wird deployt",
    "state.enrolling": "wird registriert",
    "state.error": "Fehler",
    "state.failed": "fehlgeschlagen",
    "state.fresh": "aktuell",
    "state.healthy": "gesund",
    "state.in_progress": "in Arbeit",
    "state.managed": "verwaltet",
    "state.migrating": "wird migriert",
    "state.missing": "fehlt",
    "state.not_reported": "nicht gemeldet",
    "state.observed": "beobachtet",
    "state.offline": "offline",
    "state.ok": "ok",
    "state.online": "online",
    "state.pending": "ausstehend",
    "state.pending_verification": "Verifizierung ausstehend",
    "state.planned": "geplant",
    "state.present": "vorhanden",
    "state.provisioning": "wird bereitgestellt",
    "state.reachable": "erreichbar",
    "state.ready": "bereit",
    "state.recorded": "erfasst",
    "state.revoked": "widerrufen",
    "state.rollout": "Rollout",
    "state.running": "läuft",
    "state.stale": "veraltet",
    "state.stalled": "hängt",
    "state.starting": "startet",
    "state.stopped": "gestoppt",
    "state.succeeded": "erfolgreich",
    "state.success": "Erfolg",
    "state.unexpected": "unerwartet",
    "state.unhealthy": "nicht gesund",
    "state.unknown": "unbekannt",
    "ui.accordion.advancedSettings": "Erweiterte Einstellungen",
    "ui.aiHandover.couldNotOpen":
      "kombify AI konnte nicht geöffnet werden: {error}",
    "ui.api.networkErrorCouldNotConnect":
      "Netzwerkfehler: Verbindung zur Discovery-API nicht möglich. Läuft das Backend?",
    "ui.api.scanPollingTimeout": "Zeitüberschreitung beim Abfragen des Scans",
    "ui.api.serverSideAPIBaseURL":
      "Die serverseitige API-Basis-URL ist nicht konfiguriert. Setze TECHSTACK_API_URL (Laufzeit) oder VITE_API_URL (Build-Zeit).",
    "ui.argon2.addNumber": "Füge mindestens eine Zahl hinzu.",
    "ui.argon2.addSymbols":
      "Füge Leerzeichen oder Symbole hinzu, damit die Passphrase schwerer zu erraten ist.",
    "ui.argon2.longer":
      "Längere Passphrasen sind leichter zu merken und stärker.",
    "ui.argon2.tooShort": "Passphrase zu kurz (mindestens {min} Zeichen)",
    "ui.argon2.useAtLeast": "Verwende mindestens {min} Zeichen.",
    "ui.auth.authInitFailed": "Auth-Initialisierung fehlgeschlagen",
    "ui.auth.cloudAuthenticationNotConfigured":
      "Cloud-Authentifizierung nicht konfiguriert",
    "ui.auth.invalidEmailOrPassword":
      "Ungültige E-Mail oder ungültiges Passwort",
    "ui.auth.portalSignInDidNot":
      "Die Portal-Anmeldung hat keine verifizierte Browsersitzung hergestellt",
    "ui.authCloud-link-complete.closeThisTabAndTry":
      "Schließe diesen Tab und versuche es erneut im Assistenten.",
    "ui.authCloud-link-complete.kombifyCloudConnected":
      "kombify Cloud verbunden",
    "ui.authCloud-link-complete.kombifyCloudLinkKombifyTechstack":
      "kombify Cloud Link - kombify-Techstack",
    "ui.authCloud-link-complete.thisWindowClosesAutomatically":
      "Dieses Fenster schließt sich automatisch.",
    "ui.authCloud-link-complete.youCanCloseThisTab":
      "Du kannst diesen Tab schließen und zum Assistenten zurückkehren.",
    "ui.authHandler.yourSessionHasExpiredPlease":
      "Deine Sitzung ist abgelaufen. Bitte melde dich erneut an.",
    "ui.authSso.noSsoTokenProvided": "Kein SSO-Token angegeben",
    "ui.authSso.ssoAuthenticationFailed":
      "SSO-Authentifizierung fehlgeschlagen",
    "ui.authSso.ssoAuthenticationKombifyTechstack":
      "SSO-Authentifizierung - kombify-Techstack",
    "ui.authSso.unableToVerifyYourCredentials":
      "Deine Zugangsdaten konnten nicht überprüft werden",
    "ui.authSso.yourKombifyCloudSessionCould":
      "Deine kombify-Cloud-Sitzung konnte nicht wiederhergestellt werden. Bitte versuche es erneut.",
    "ui.client.csrfTokenResponseMissingToken":
      "Der CSRF-Token-Antwort fehlt das Token",
    "ui.client.gatewayAuthenticationUnavailableSignIn":
      "Gateway-Authentifizierung nicht verfügbar. Melde dich erneut an, um deine Berechtigungen für die verwaltete Runtime zu verifizieren.",
    "ui.client.networkError":
      "Netzwerkfehler: Verbindung zur API nicht möglich. Ist das Backend erreichbar?",
    "ui.client.networkErrorAt":
      "Netzwerkfehler: Verbindung zur API unter {base} nicht möglich. Ist das Backend erreichbar?",
    "ui.client.parentGatewayTokenFailed":
      "Token des übergeordneten Gateways fehlgeschlagen",
    "ui.client.requestTimedOutAfterS":
      "Zeitüberschreitung der Anfrage nach {v1} s: {url}",
    "ui.client.serverReturnedHTMLInsteadOf":
      "{basePrefix}Der Server hat HTML statt JSON zurückgegeben ({status}). Ist das Backend erreichbar und bist du authentifiziert?",
    "ui.clientLocal.checkingLocalAuthState":
      "Lokaler Anmeldestatus wird geprüft...",
    "ui.clientLocal.continueLocalSetup": "Lokale Einrichtung fortsetzen",
    "ui.clientLocal.createOrSignInAs":
      "Erstelle den lokalen Owner für dieses selbst gehostete Techstack oder melde dich als dieser an. Nach der Einrichtung öffnet sich direkt die Operator-Oberfläche.",
    "ui.clientLocal.createTheFirstLocalAdmin":
      "Erstelle den ersten lokalen Admin für dieses Gerät. Nach der Einrichtung öffnet dieser Windows-Client direkt die Operator-Oberfläche.",
    "ui.clientLocal.creatingLocalOwner": "Lokaler Owner wird erstellt...",
    "ui.clientLocal.email": "E-Mail",
    "ui.clientLocal.firstLocalAdmin": "Erster lokaler Admin",
    "ui.clientLocal.localOwnerSignIn": "Anmeldung des lokalen Owners",
    "ui.clientLocal.localOwnerSignedIn": "Lokaler Owner angemeldet.",
    "ui.clientLocal.localOwnerWasCreatedBut":
      "Der lokale Owner wurde erstellt, aber die Anmeldung ist fehlgeschlagen.",
    "ui.clientLocal.localSetupFailed": "Lokale Einrichtung fehlgeschlagen.",
    "ui.clientLocal.localSignInFailed": "Lokale Anmeldung fehlgeschlagen.",
    "ui.clientLocal.localTechstackSetup": "Lokale Techstack-Einrichtung.",
    "ui.clientLocal.localTechstackSetupKombifyTechstack":
      "Lokale Techstack-Einrichtung | kombify Techstack",
    "ui.clientLocal.ownerName": "Name des Owners: {name}",
    "ui.clientLocal.password": "Passwort",
    "ui.clientLocal.selfHostedLocalOwner": "Lokaler Owner (selbst gehostet)",
    "ui.clientLocal.signInLocally": "Lokal anmelden",
    "ui.clientLocal.signingIn": "Anmeldung läuft...",
    "ui.clientLocal.thisDeviceIsAlreadyConfigured":
      "Dieses Gerät ist bereits eingerichtet. Melde dich mit dem Konto des lokalen Owners an, um Techstack zu öffnen.",
    "ui.clientOnboarding.connectAnExistingSelfHosted":
      "Einen bestehenden selbst gehosteten Server verbinden",
    "ui.clientOnboarding.connectServer": "Server verbinden",
    "ui.clientOnboarding.installLocally": "Lokal installieren",
    "ui.clientOnboarding.localWindowsInstallation":
      "Lokale Windows-Installation",
    "ui.clientOnboarding.noTechstackAccountRequiredTechstack":
      "Kein Techstack-Konto erforderlich. Techstack läuft auf diesem Gerät und öffnet einen tokengeschützten Enrollment-Kanal für dein privates Netzwerk.",
    "ui.clientOnboarding.setUpKombifyTechstackOn":
      "kombify Techstack auf diesem Windows-Gerät einrichten.",
    "ui.clientOnboarding.signInWithKombifyCloud": "Mit kombify Cloud anmelden",
    "ui.clientOnboarding.signInWithYourKombify":
      "Melde dich mit deinem kombify-Cloud-Konto an und verbinde diesen Desktop-Client.",
    "ui.clientOnboarding.startingLocally": "Lokaler Start läuft...",
    "ui.clientOnboarding.thePrivateNetworkEnrollmentChannel":
      "Der Enrollment-Kanal für das private Netzwerk konnte nicht aktiviert werden.",
    "ui.clientOnboarding.thisClientIsTheLocal":
      "Dieser Client ist der lokale Desktop-Einstieg zur Orchestrierung deiner eigenen Server und StackKits. Standardmäßig wird Techstack lokal auf diesem Gerät installiert.",
    "ui.clientOnboarding.useLocally": "Lokal nutzen",
    "ui.clientOnboarding.useYourOwnSelfHosted":
      "Stattdessen deinen eigenen selbst gehosteten Server verwenden",
    "ui.clientOnboarding.windowsClientOnboardingKombifyTechstack":
      "Windows-Client-Onboarding | kombify Techstack",
    "ui.cockpitSnapshot.monitoringCockpitCouldNotBe":
      "Das Monitoring-Cockpit konnte nicht aktualisiert werden.",
    "ui.common.copiedCheck": "✓ Kopiert",
    "ui.common.copy": "Kopieren",
    "ui.common.sourceValue": "Quelle: {value}",
    "ui.common.unknown": "unbekannt",
    "ui.completeDashboard.allServices": "Alle Services →",
    "ui.completeDashboard.nodesApps": "Nodes & Apps",
    "ui.configFlow.apiUnreachable":
      "Der API-Server ist von deinem Browser aus nicht erreichbar.",
    "ui.configFlow.apiUnreachableAt":
      "Der API-Server unter {base} ist von deinem Browser aus nicht erreichbar.",
    "ui.configFlow.capability": "Fähigkeit: {value}",
    "ui.configFlow.corsHint":
      "Wenn das Backend läuft, der Browser die Anfrage aber blockiert (CORS), öffne DevTools → Konsole und suche nach einem CORS-Fehler.",
    "ui.configFlow.createFailedAt": "Erstellen fehlgeschlagen bei: {phase}",
    "ui.configFlow.deploymentNameExists":
      'In deinem Homelab existiert bereits ein StackKit-Deployment mit dem Namen "{name}". Öffne das bestehende Deployment oder wähle einen anderen Namen.',
    "ui.configFlow.errorCode": "Fehlercode: {value}",
    "ui.configFlow.missingFeatures": "Fehlende Funktionen: {value}",
    "ui.configFlow.nextStep": "Nächster Schritt: {value}",
    "ui.configFlow.nextSteps": "Nächste Schritte:",
    "ui.configFlow.pleaseFix": "Bitte behebe Folgendes:",
    "ui.configFlow.provider": "Anbieter: {value}",
    "ui.configFlow.reason": "Grund: {value}",
    "ui.configFlow.requestId": "Request-ID: {value}",
    "ui.configFlow.requiredFeatures": "Erforderliche Funktionen: {value}",
    "ui.configFlow.serverEncountered":
      "Der Server hat einen Fehler festgestellt ({status}). {message}",
    "ui.configFlow.stackId": "Stack-ID: {value}",
    "ui.configFlow.stackkitsValidation": "StackKits-Validierung:",
    "ui.configFlow.step": "Schritt: {value}",
    "ui.configFlow.technicalDetails": "Technische Details: {value}",
    "ui.configurationFlow.aPreviousSubmissionAlreadyCompleted":
      "Eine frühere Übermittlung wurde bereits abgeschlossen",
    "ui.configurationFlow.anEarlierSubmissionFromThis":
      "Eine frühere Übermittlung aus dieser Browsersitzung wurde bereits mit anderen Antworten abgeschlossen. Es wurde ein neuer Versuchsschlüssel erzeugt – sende erneut ab oder öffne das bestehende Deployment.",
    "ui.configurationFlow.anUnexpectedErrorOccurredPlease":
      "Ein unerwarteter Fehler ist aufgetreten. Details findest du in der Browserkonsole.",
    "ui.configurationFlow.authenticationRequired":
      "Authentifizierung erforderlich",
    "ui.configurationFlow.cannotConnectToBackend":
      "Keine Verbindung zum Backend",
    "ui.configurationFlow.deploymentFailed": "Deployment fehlgeschlagen",
    "ui.configurationFlow.managedServerIsNotActive":
      "Verwalteter Server ist nicht aktiv",
    "ui.configurationFlow.permissionDenied": "Zugriff verweigert",
    "ui.configurationFlow.retryingIsUnlikelyToHelp":
      "Ein erneuter Versuch hilft voraussichtlich erst, wenn das serverseitige Problem behoben ist.",
    "ui.configurationFlow.serverError": "Serverfehler",
    "ui.configurationFlow.stackkitDeploymentNameAlreadyExists":
      "Name des StackKit-Deployments existiert bereits",
    "ui.configurationFlow.theBackendRejectedThisStackkit":
      "Das Backend hat diese StackKit-Deployment-Anfrage wegen eines Konflikts abgelehnt.",
    "ui.configurationFlow.useTheAdminPasswordConfigured":
      "(verwende das für diese Instanz konfigurierte Admin-Passwort)",
    "ui.configurationFlow.validatingConfiguration":
      "Konfiguration wird validiert...",
    "ui.configurationFlow.validatingWithStackkits":
      "Validierung mit StackKits...",
    "ui.configurationFlow.youCanRetryIfThis":
      "Du kannst es erneut versuchen. Falls es wieder passiert, teile dem Support den Fehlercode und die Request-ID mit.",
    "ui.configurationFlow.youDonTHavePermission":
      "Du hast keine Berechtigung, StackKits zu deployen, und der Server hat keinen konkreten Grund genannt. Bitte teile das dem Support mit.",
    "ui.configurationFlow.youMustBeLoggedIn":
      "Du musst angemeldet sein, um ein StackKit zu deployen.",
    "ui.configurationFlow.youMustBeLoggedIn2":
      "Du musst angemeldet sein, um ein StackKit zu deployen. Bitte melde dich zuerst an.",
    "ui.connectDevices.techstackClient": "Techstack-Client",
    "ui.creationCompletion.ownerSeedFor":
      "Der Owner-Seed für {email} ist vorbereitet.",
    "ui.creationController.theAdditionalNode": "Der zusätzliche Node",
    "ui.creationFailure.lease": "Lease",
    "ui.creationInstall.previewExpires": "Vorschau läuft um {time} ab",
    "ui.creationLease.sshHost":
      "kombify verwendet die erfassten SSH-Verbindungsdaten für den bestehenden Server unter {host}.",
    "ui.creationLease.sshHostUser":
      "kombify verwendet die erfassten SSH-Verbindungsdaten für den bestehenden Server unter {host} als {user}.",
    "ui.creationLease.sshUser":
      "kombify verwendet die erfassten SSH-Verbindungsdaten für den bestehenden Server als {user}.",
    "ui.creationRequirements.minCpu": "CPU: mind. {count} Kerne",
    "ui.creationRequirements.minRam": "RAM: mind. {size} GB",
    "ui.creationRequirements.minVersion": "mind.: {version}",
    "ui.creationRun.notConnectedYet": "Noch nicht verbunden",
    "ui.creationRun.notReachableYet": "Noch nicht erreichbar",
    "ui.creationRun.pairingTokenExpires":
      "Das Pairing-Token läuft um {slot} ab.",
    "ui.creationRun.resumeStartingAt":
      "Ab {time} kann der Server eine sichere Wiederaufnahme auf derselben VM autorisieren.",
    "ui.creationRun.stepOf": "Schritt {step} von {total}",
    "ui.credentialForm.accessEntriesNeedAtLeast":
      "Zugangseinträge benötigen mindestens einen Benutzer, eine URL oder ein Secret",
    "ui.credentialForm.administrativeToolsAndOperationalSurfaces":
      "Administrationswerkzeuge und Betriebsoberflächen.",
    "ui.credentialForm.apiKey": "API-Schlüssel",
    "ui.credentialForm.apiLabelOwner": "API-Bezeichnung / Owner",
    "ui.credentialForm.bearerTokenOrRefreshToken":
      "Bearer-Token oder Refresh-Token...",
    "ui.credentialForm.breakGlassSecretsAndReveal":
      "Break-Glass-Secrets und Wiederherstellungsmaterial, das nur angezeigt werden kann.",
    "ui.credentialForm.cert": "Zert.",
    "ui.credentialForm.certificate": "Zertifikat",
    "ui.credentialForm.certificateContent": "Zertifikatsinhalt",
    "ui.credentialForm.credentialType": "Art der Zugangsdaten",
    "ui.credentialForm.describeTheRecoveryPathStorage":
      "Beschreibe den Wiederherstellungsweg, den Speicherort oder Anweisungen für Operatoren...",
    "ui.credentialForm.describeTheUserGateOr":
      "Beschreibe den Benutzer, das Gate oder die Zugriffsrichtlinie, zu der dieser Eintrag gehört...",
    "ui.credentialForm.eGBreakGlassEnvelope": "z. B. Break-Glass-Umschlag",
    "ui.credentialForm.eGPocketbaseAdmin": "z. B. PocketBase-Admin",
    "ui.credentialForm.eGTailscaleDeviceApproval":
      "z. B. Tailscale-Geräte-Freigabe",
    "ui.credentialForm.enterPassword": "Passwort eingeben...",
    "ui.credentialForm.enterSecretValue": "Secret-Wert eingeben...",
    "ui.credentialForm.expiryDate": "Ablaufdatum",
    "ui.credentialForm.failedToSaveCredential":
      "Zugangsdaten konnten nicht gespeichert werden",
    "ui.credentialForm.keyIdentifierLabel": "Schlüsselkennung / Bezeichnung",
    "ui.credentialForm.name": "Name",
    "ui.credentialForm.nameIsRequired": "Name ist erforderlich",
    "ui.credentialForm.notes": "Notizen",
    "ui.credentialForm.oauthToken": "OAuth-Token",
    "ui.credentialForm.operatorAccount": "Operator / Konto",
    "ui.credentialForm.optional": "(optional)",
    "ui.credentialForm.otpauthTotpOrBase32Secret":
      "otpauth://totp/... oder Base32-Secret",
    "ui.credentialForm.portalPolicyUrl": "Portal- / Richtlinien-URL",
    "ui.credentialForm.recoveryUrlEndpoint":
      "Wiederherstellungs-URL / Endpunkt",
    "ui.credentialForm.saving": "Wird gespeichert...",
    "ui.credentialForm.sshKey": "SSH-Schlüssel",
    "ui.credentialForm.token": "Token",
    "ui.credentialForm.toolUrl": "Tool-URL",
    "ui.credentialForm.toolUrlIsRequired": "Tool-URL ist erforderlich",
    "ui.credentialForm.userIdentity": "Benutzer / Identität",
    "ui.credentialForm.usernameEmail": "Benutzername / E-Mail",
    "ui.credentialForm.usersPortalsIpDeviceGating":
      "Benutzer, Portale, IP-/Geräte-Gating und Zugriffskontrollen.",
    "ui.credentialForm.walletArea": "Wallet-Bereich",
    "ui.credentialForm.whatThisToolIsFor":
      "Wofür dieses Tool dient, wer es nutzt und Hinweise zum Start...",
    "ui.crypto.decryptionFailedWrongPassword":
      "Entschlüsselung fehlgeschlagen. Falsches Passwort?",
    "ui.custody.leasesWithoutNode.one": "{count} Lease ohne Node",
    "ui.custody.leasesWithoutNode.other": "{count} Leases ohne Node",
    "ui.custody.wasAddress": "war {ip}",
    "ui.custodyLeasesPanel.decommissioning": "Außerbetriebnahme läuft...",
    "ui.custodyLeasesPanel.leaseArchived": "Lease archiviert",
    "ui.custodyLeasesPanel.leaseCancelled": "Lease storniert",
    "ui.custodyLeasesPanel.neverObserved": "Nie beobachtet",
    "ui.custodyLeasesPanel.noExecutionAuthorityLegacyOr":
      "Keine Ausführungsberechtigung (veraltetes oder ungebundenes Lease)",
    "ui.custodyLeasesPanel.nodeNeverFinishedEnrolling":
      "Node hat die Registrierung nie abgeschlossen",
    "ui.custodyLeasesPanel.resolving": "Wird aufgelöst...",
    "ui.custodyLeasesPanel.vmNoLongerExistsAt":
      "VM existiert beim Anbieter nicht mehr",
    "ui.dashboardServiceSheet.noHealthReported": "Kein Health-Status gemeldet",
    "ui.discovery.addCredentials.one": "{count} Zugangsdaten hinzufügen",
    "ui.discovery.addCredentials.other": "{count} Zugangsdaten hinzufügen",
    "ui.discovery.selected": "{count} ausgewählt",
    "ui.easyWizard.pleaseSelectAnAccessMode":
      "Bitte wähle einen Zugriffsmodus (Nur zu Hause oder Überall)",
    "ui.easyWizard.pleaseSelectWhoWillUse":
      "Bitte wähle aus, wer deinen Server nutzen wird",
    "ui.easyWizard.serverHostOrIpIs":
      "Für die direkte Verbindung sind Server-Host oder IP erforderlich",
    "ui.errors.anUnknownErrorOccurred":
      "Ein unbekannter Fehler ist aufgetreten",
    "ui.export.decryptionNotAvailablePleaseUse":
      "Entschlüsselung nicht verfügbar. Bitte nutze einen modernen Browser mit HTTPS.",
    "ui.export.encryptionNotAvailablePleaseUse":
      "Verschlüsselung nicht verfügbar. Bitte nutze einen modernen Browser mit HTTPS.",
    "ui.export.failedToParseExportFile":
      "Exportdatei konnte nicht gelesen werden",
    "ui.export.invalidCredentialMissingNameOr":
      "Ungültige Zugangsdaten: Name oder Art fehlt",
    "ui.export.invalidExportFileMalformedJSON":
      "Ungültige Exportdatei: fehlerhaftes JSON",
    "ui.export.invalidExportFileStructure":
      "Ungültige Struktur der Exportdatei",
    "ui.export.invalidExportFormatExpectedArray":
      "Ungültiges Exportformat: Array erwartet",
    "ui.export.passwordRequiredForEncryptedExport":
      "Für den verschlüsselten Export ist ein Passwort erforderlich",
    "ui.export.unsupportedExportVersionExpected":
      "Nicht unterstützte Exportversion: {version}. Erwartet: {EXPORT_VERSION}",
    "ui.featureGate.enableThisFeatureInSettings":
      "Aktiviere diese Funktion unter Einstellungen → Funktionen",
    "ui.featureGate.grantConsentInSettingsFeatures":
      "Erteile deine Zustimmung unter Einstellungen → Funktionen, um sie zu aktivieren",
    "ui.featureGate.thisFeatureRequiresAdminPrivileges":
      "Diese Funktion erfordert Admin-Rechte",
    "ui.features.failedToLoadFeatures":
      "Funktionen konnten nicht geladen werden",
    "ui.footer.copyright": "© {year} Kombiverse Labs",
    "ui.footer.copyrightTagline":
      "© {year} Kombiverse Labs. Für Menschen gemacht, von Intelligenz angetrieben.",
    "ui.footer.forHomelabCommunity": "für die Homelab-Community",
    "ui.footer.instanceShort": "Instanz:{id}",
    "ui.footer.instanceTitle": "Instanz {id}",
    "ui.footer.toggleLogoStyle": "Logo-Stil umschalten ({style})",
    "ui.footerModern.about": "Über uns",
    "ui.footerModern.contact": "Kontakt",
    "ui.footerModern.docs": "Doku",
    "ui.footerModern.features": "Funktionen",
    "ui.footerModern.madeWith": "Gemacht mit",
    "ui.footerModern.privacy": "Datenschutz",
    "ui.footerModern.terms": "Bedingungen",
    "ui.groupedTaskList.done": "Erledigt",
    "ui.homelab.agentVersions.one": "Gemeldete Agent-Version: {versions}.",
    "ui.homelab.agentVersions.other": "Gemeldete Agent-Versionen: {versions}.",
    "ui.homelab.connectedOfTotal": "{connected}/{total} verbunden",
    "ui.homelab.latestFailed": "Letzte Aktion {type} fehlgeschlagen — {detail}",
    "ui.homelab.noSourceObserved":
      "Services stammen aus zwei Quellen: einem abgeschlossenen StackKit-Rollout sowie den Containern und Units, die der Agent auf dem Host erkennt. Bisher wurde keine der beiden Quellen beobachtet: Es wurde kein StackKit-Manifest gefunden und der Agent hat keine Service-Erkennung ausgeführt.",
    "ui.homelab.onlineCount": "{count} online",
    "ui.homelab.operationsNotLoaded.one":
      "{count} StackKit-Deployment-Vorgang konnte nicht geladen werden.",
    "ui.homelab.operationsNotLoaded.other":
      "{count} StackKit-Deployment-Vorgänge konnten nicht geladen werden.",
    "ui.homelab.partialRolloutBadge": "teilweiser Rollout",
    "ui.homelab.phase": "Phase: {phase}",
    "ui.homelab.preparationFailed":
      "StackKit-Vorbereitung auf verbundenem Node fehlgeschlagen — {detail}",
    "ui.homelab.probeAnswered":
      "Die Runtime hat auf die Prüfung geantwortet und der Guard-Heartbeat ist aktuell ({state}).",
    "ui.homelab.probeOffline":
      'Der Anbieter meldet die Maschine als "{machineState}" und die Registrierung als "{enrollment}". Erneut verbinden kann nur diese Prüfung wiederholen – den kombify Agent auf der Maschine kann es nicht neu starten. Der Node bleibt offline, bis der Agent wieder einen Heartbeat sendet. Fahre daher in den Node-Details fort, dort werden der Enrollment-Befehl und der letzte Kontakt angezeigt.',
    "ui.homelab.recorded": "{count} erfasst",
    "ui.homelab.retryFailed":
      "Rollout konnte nicht wiederholt werden: {message}",
    "ui.homelab.rolloutFailed": "Rollout fehlgeschlagen: {message}",
    "ui.homelab.sshFailed":
      "SSH-Verbindung zu deinem Node fehlgeschlagen — {detail}",
    "ui.homelabDashboardPage.0VerifiedConnectedOperationsEvidence":
      "0 verifiziert verbunden · keine Betriebsnachweise verfügbar",
    "ui.homelabDashboardPage.agentDiscoveryReportedNoRunning":
      "Die Agent-Erkennung hat keine laufenden Container oder Units gemeldet. Es wurde kein StackKit-Manifest beobachtet, daher sind deklarierte StackKit-Services weiterhin unbekannt.",
    "ui.homelabDashboardPage.bothServiceSourcesReportedAn":
      "Beide Service-Quellen haben ein leeres Inventar gemeldet: Das StackKit-Manifest enthielt keine Services und die Agent-Erkennung hat keine laufenden Container oder Units gefunden.",
    "ui.homelabDashboardPage.confirmThatTheProviderResource":
      "Bestätige, dass die Ressource beim Anbieter bereits entfernt wurde. Techstack archiviert nur den veralteten Custody-Eintrag und löscht keine Ressource beim Anbieter.",
    "ui.homelabDashboardPage.custodyRecordUnchangedTheTechstack":
      "Custody-Eintrag unverändert: Das Techstack-Gateway konnte das Backend nicht erreichen. Wiederhole genau diesen Eintrag, sobald der Dienst verfügbar ist.",
    "ui.homelabDashboardPage.custodyResolutionFailed":
      "Custody-Auflösung fehlgeschlagen",
    "ui.homelabDashboardPage.decommissionFailed":
      "Außerbetriebnahme fehlgeschlagen",
    "ui.homelabDashboardPage.deployStackkit": "StackKit deployen",
    "ui.homelabDashboardPage.lastVerifiedStateRetainedThe":
      "Letzter verifizierter Stand beibehalten — die Aktualisierung ist fehlgeschlagen und wird automatisch wiederholt.",
    "ui.homelabDashboardPage.managedRuntime": "Verwaltete Runtime",
    "ui.homelabDashboardPage.managedRuntimeLeaseAllocationMetadata":
      "Lease-/Zuteilungs-Metadaten der verwalteten Runtime sind vorhanden, aber der aktuelle Anbieter- und Guard-Status konnte nicht verifiziert werden.",
    "ui.homelabDashboardPage.noNodeHasReportedAn":
      "Noch kein Node hat ein Inventar gemeldet.",
    "ui.homelabDashboardPage.nodeIsReportingAgain": "Node meldet sich wieder",
    "ui.homelabDashboardPage.openTheNodeDetailsTo":
      "Öffne die Node-Details, um den Agent und den letzten Rollout zu prüfen.",
    "ui.homelabDashboardPage.operationsDataIsNotAvailable":
      "Für dieses Homelab sind noch keine Betriebsdaten verfügbar.",
    "ui.homelabDashboardPage.operationsDataIsNotAvailable2":
      "Betriebsdaten noch nicht verfügbar",
    "ui.homelabDashboardPage.reconnect": "Erneut verbinden",
    "ui.homelabDashboardPage.reconnectFailed":
      "Erneutes Verbinden fehlgeschlagen",
    "ui.homelabDashboardPage.reconnecting":
      "Verbindung wird wiederhergestellt...",
    "ui.homelabDashboardPage.refresh": "Aktualisieren",
    "ui.homelabDashboardPage.resolveExactRecord": "Genauen Eintrag auflösen",
    "ui.homelabDashboardPage.resolveRecord": "Eintrag auflösen",
    "ui.homelabDashboardPage.resolveStaleCustodyRecord":
      "Veralteten Custody-Eintrag auflösen?",
    "ui.homelabDashboardPage.retry": "Erneut versuchen",
    "ui.homelabDashboardPage.retryExactCleanup":
      "Genaue Bereinigung wiederholen",
    "ui.homelabDashboardPage.review": "Prüfen",
    "ui.homelabDashboardPage.reviewStart": "Prüfen + Starten",
    "ui.homelabDashboardPage.rolloutReadinessIsUnavailableRefresh":
      "Die Rollout-Bereitschaft ist nicht verfügbar. Aktualisiere die Betriebsdaten, bevor du startest.",
    "ui.homelabDashboardPage.runtimeAnsweredButTheNode":
      "Runtime hat geantwortet, aber der Node ist weiterhin offline",
    "ui.homelabDashboardPage.stackkitDeployment": "StackKit-Deployment",
    "ui.homelabDashboardPage.starting": "Wird gestartet...",
    "ui.homelabDashboardPage.theAgentVersionWasNot":
      "Die Agent-Version wurde nicht gemeldet.",
    "ui.homelabDashboardPage.theFailedCleanupIsNot":
      "Die fehlgeschlagene Bereinigung ist an kein aktionsfähiges Lease gebunden. Aktualisiere die Lifecycle-Nachweise und nutze die Aktion am genauen Custody-Eintrag.",
    "ui.homelabDashboardPage.theFailedStackkitDeploymentIs":
      "Das fehlgeschlagene StackKit-Deployment gehört nicht mehr zu diesem Homelab. Aktualisiere das Homelab und wiederhole genau dieses Deployment.",
    "ui.homelabDashboardPage.thePersistedRolloutIsWaiting":
      "Der gespeicherte Rollout wartet auf seinen nächsten Checkpoint.",
    "ui.homelabDashboardPage.theRuntimeProbeReturnedNo":
      "Die Runtime-Prüfung hat keinen Grund geliefert. Versuche es erneut oder öffne die Node-Details für den vollständigen Runtime-Eintrag.",
    "ui.homelabDashboardPage.theStackkitManifestSourceReported":
      "Die StackKit-Manifest-Quelle hat keine Services gemeldet. Die Service-Erkennung wurde nicht beobachtet, daher sind Container und Units weiterhin unbekannt.",
    "ui.homelabDashboardPage.thisFailedRunCannotBe":
      "Dieser fehlgeschlagene Lauf kann nicht automatisch und sicher wiederholt werden.",
    "ui.homelabDashboardPage.you": "Du",
    "ui.homelabDashboardPage.yourHomelab": "Dein Homelab",
    "ui.homelabModel.lifecycleConnectionHealth":
      "Lifecycle {lifecycle} · Verbindung {connection} · Health {health}",
    "ui.homelabModel.managedVPS": "Verwalteter VPS",
    "ui.homelabModel.ownDevice": "Eigenes Gerät",
    "ui.hypervisor.diskGiB": "{label} (GiB)",
    "ui.hypervisor.storageAvail": "{name} · {size} GiB",
    "ui.hypervisorSelection.homeAssistantOs": "Home Assistant OS",
    "ui.hypervisorSelection.ramMib": "RAM (MiB)",
    "ui.identity.alien": "Alien",
    "ui.identity.astronaut": "Astronaut",
    "ui.identity.circuit": "Schaltkreis",
    "ui.identity.cybershield": "Cyberschild",
    "ui.identity.dragon": "Drache",
    "ui.identity.ghost": "Geist",
    "ui.identity.hologram": "Hologramm",
    "ui.identity.myHomelab": "Mein Homelab",
    "ui.identity.nebula": "Nebel",
    "ui.identity.ninja": "Ninja",
    "ui.identity.phoenix": "Phönix",
    "ui.identity.quantum": "Quant",
    "ui.identity.robot": "Roboter",
    "ui.identity.rocket": "Rakete",
    "ui.identity.satellite": "Satellit",
    "ui.identity.unicorn": "Einhorn",
    "ui.identity.wizard": "Zauberer",
    "ui.importExport.downloadEncrypted.one":
      "{count} Zugangsdaten als verschlüsseltes JSON herunterladen",
    "ui.importExport.downloadEncrypted.other":
      "{count} Zugangsdaten als verschlüsseltes JSON herunterladen",
    "ui.importExport.exportedSummary": "Exportiert: {date} • {count} Einträge",
    "ui.importExport.notEncrypted":
      "{format}-Exporte sind nicht verschlüsselt. Bewahre die Datei sicher auf.",
    "ui.importExport.readyToImport.one":
      "Bereit zum Importieren von {count} Zugangsdaten",
    "ui.importExport.readyToImport.other":
      "Bereit zum Importieren von {count} Zugangsdaten",
    "ui.importExport.willBeExported.one":
      "{count} Zugangsdaten werden exportiert.",
    "ui.importExport.willBeExported.other":
      "{count} Zugangsdaten werden exportiert.",
    "ui.importExportModal.back": "Zurück",
    "ui.importExportModal.bitwardenExportFailed":
      "Bitwarden-Export fehlgeschlagen",
    "ui.importExportModal.bitwardenJson": "Bitwarden JSON",
    "ui.importExportModal.cancel": "Abbrechen",
    "ui.importExportModal.chooseDifferentFile": "Andere Datei wählen",
    "ui.importExportModal.clickToSelectFile":
      "Klicken, um eine Datei auszuwählen",
    "ui.importExportModal.compatibleWithKeepassLastpassAnd":
      "Kompatibel mit KeePass, LastPass und generischen Passwortmanagern.",
    "ui.importExportModal.confirmPassword": "Passwort bestätigen",
    "ui.importExportModal.csvExportFailed": "CSV-Export fehlgeschlagen",
    "ui.importExportModal.decrypt": "Entschlüsseln",
    "ui.importExportModal.decryptionFailed": "Entschlüsselung fehlgeschlagen",
    "ui.importExportModal.decryptionPassword": "Entschlüsselungspasswort",
    "ui.importExportModal.download": "Herunterladen",
    "ui.importExportModal.encryptExport": "Export verschlüsseln",
    "ui.importExportModal.encryptionNotAvailableRequiresHttps":
      "Verschlüsselung nicht verfügbar (erfordert HTTPS)",
    "ui.importExportModal.encryptionPassword": "Verschlüsselungspasswort",
    "ui.importExportModal.enterExportPassword": "Export-Passwort eingeben",
    "ui.importExportModal.exportCredentials": "Zugangsdaten exportieren",
    "ui.importExportModal.exportFailed": "Export fehlgeschlagen",
    "ui.importExportModal.exportFormat": "Exportformat",
    "ui.importExportModal.exportWallet": "Wallet exportieren",
    "ui.importExportModal.exporting": "Wird exportiert...",
    "ui.importExportModal.exportingWithoutEncryptionWillSave":
      "Ein Export ohne Verschlüsselung speichert alle Secrets im Klartext. Nutze das nur zum Testen.",
    "ui.importExportModal.failedToReadFile":
      "Datei konnte nicht gelesen werden",
    "ui.importExportModal.import": "Importieren",
    "ui.importExportModal.importCredentials": "Zugangsdaten importieren",
    "ui.importExportModal.importDirectlyIntoBitwardenOr":
      "Direkt in Bitwarden oder kompatible Tresore importieren.",
    "ui.importExportModal.importExportCredentials":
      "Zugangsdaten importieren / exportieren",
    "ui.importExportModal.importFailed": "Import fehlgeschlagen",
    "ui.importExportModal.importWallet": "Wallet importieren",
    "ui.importExportModal.important": "Wichtig:",
    "ui.importExportModal.importedCredentialsWillBeAdded":
      "Importierte Zugangsdaten werden als neue Einträge hinzugefügt. Duplikate werden nicht automatisch zusammengeführt.",
    "ui.importExportModal.importing": "Wird importiert...",
    "ui.importExportModal.keepThisPasswordSafeWithout":
      "Bewahre dieses Passwort sicher auf! Ohne es kannst du deine Zugangsdaten nicht wiederherstellen.",
    "ui.importExportModal.kombifyTechstackJson": "kombify-Techstack JSON",
    "ui.importExportModal.kombifyTechstackWalletExportJson":
      "kombify-Techstack-Wallet-Export (.json)",
    "ui.importExportModal.min8Characters": "Mind. 8 Zeichen",
    "ui.importExportModal.nativeFormatWithEncryptionSupport":
      "Natives Format mit Verschlüsselungsunterstützung. Ideal für Backup und Wiederherstellung.",
    "ui.importExportModal.note": "Hinweis:",
    "ui.importExportModal.passwordIsRequired": "Passwort ist erforderlich",
    "ui.importExportModal.passwordIsRequiredForEncrypted":
      "Für den verschlüsselten Export ist ein Passwort erforderlich",
    "ui.importExportModal.passwordMustBeAtLeast":
      "Das Passwort muss mindestens 8 Zeichen lang sein",
    "ui.importExportModal.passwordsDoNotMatch":
      "Die Passwörter stimmen nicht überein",
    "ui.importExportModal.reEnterPassword": "Passwort erneut eingeben",
    "ui.importExportModal.restoreCredentialsFromABackup":
      "Zugangsdaten aus einer Backup-Datei wiederherstellen",
    "ui.importExportModal.thisExportIsEncryptedEnter":
      "Dieser Export ist verschlüsselt. Gib das Passwort zum Entschlüsseln ein.",
    "ui.importExportModal.universalCsv": "Universelles CSV",
    "ui.importExportModal.warning": "Warnung:",
    "ui.inAppDialog.confirm": "Bestätigen",
    "ui.inAppDialog.continue": "Weiter",
    "ui.integration.admin": "Admin",
    "ui.integration.grafanaDashboardCredentials":
      "Zugangsdaten für das Grafana-Dashboard",
    "ui.integration.headscaleAPIKeyForManagement":
      "Headscale-API-Schlüssel für die Verwaltung",
    "ui.integration.pocketbaseAdminDashboardCredentials":
      "Zugangsdaten für das PocketBase-Admin-Dashboard",
    "ui.integration.traefikDashboardBasicAuth":
      "Basic Auth für das Traefik-Dashboard",
    "ui.inventory.nodeCount.one": "{count} Node",
    "ui.inventory.nodeCount.other": "{count} Nodes",
    "ui.inventory.serviceCount.one": "{count} Service",
    "ui.inventory.serviceCount.other": "{count} Services",
    "ui.inventory.telemetryNodes.one": "{count} Telemetrie-Node",
    "ui.inventory.telemetryNodes.other": "{count} Telemetrie-Nodes",
    "ui.localOwner.localOwnerCreationIsOnly":
      "Der lokale Owner kann nur bei der Ersteinrichtung erstellt werden.",
    "ui.login.bootstrapPassword": "Bootstrap-Passwort:",
    "ui.login.breakGlassAdminIsNot":
      "Der Break-Glass-Admin ist noch nicht initialisiert.",
    "ui.login.cloudSignInIsNot":
      "Die Cloud-Anmeldung ist noch nicht konfiguriert.",
    "ui.login.continueAsLocalOwner": "Als lokaler Owner fortfahren",
    "ui.login.continueWithKombifyCloud": "Weiter mit kombify Cloud",
    "ui.login.couldNotContactBackend": "Backend konnte nicht erreicht werden.",
    "ui.login.couldNotContactBackendWith":
      "Backend konnte nicht erreicht werden: {message}",
    "ui.login.emergencyAdmin": "Notfall-Admin",
    "ui.login.emergencyAdminIsNotInitialized":
      "Der Notfall-Admin ist noch nicht initialisiert.",
    "ui.login.emergencyEmail": "Notfall-E-Mail:",
    "ui.login.emergencyLoginFailed": "Notfall-Anmeldung fehlgeschlagen.",
    "ui.login.emergencyPassword": "Notfall-Passwort",
    "ui.login.emergencyRecoveryOnlyDayTo":
      "Nur für die Notfallwiederherstellung. Die tägliche Anmeldung läuft weiterhin über kombify Cloud oder den Weg des lokalen Owners.",
    "ui.login.kombifyCloudSignInIs":
      "Die Anmeldung über kombify Cloud ist derzeit nicht verfügbar.",
    "ui.login.loading": "Wird geladen...",
    "ui.login.openKombifyCloudInBrowser": "kombify Cloud im Browser öffnen",
    "ui.login.redirectingToKombifyCloud": "Weiterleitung zu kombify Cloud...",
    "ui.login.retryConnection": "Verbindung erneut versuchen",
    "ui.login.revealBootstrapPassword": "Bootstrap-Passwort anzeigen",
    "ui.login.revealFailed": "Anzeigen fehlgeschlagen.",
    "ui.login.revealing": "Wird angezeigt...",
    "ui.login.signInAsEmergencyAdmin": "Als Notfall-Admin anmelden",
    "ui.login.signInKombifyTechstack": "Anmelden | kombify Techstack",
    "ui.login.signInWithKombifyCloud": "Mit kombify Cloud anmelden.",
    "ui.login.signInWithTheLocal":
      "Melde dich mit dem Konto des lokalen Owners für dieses Techstack an.",
    "ui.login.techstackReconnectsThroughTheKombify":
      "Techstack verbindet sich über die kombify-Cloud-Seite neu, die diese Ansicht enthält. Innerhalb dieses Frames wird keine zweite Anmeldeseite geöffnet.",
    "ui.login.theBootstrapPasswordHasExpired":
      "Das Bootstrap-Passwort ist abgelaufen. Starte den Server neu, um ein neues zu erzeugen.",
    "ui.login.tooManyLoginAttemptsPlease":
      "Zu viele Anmeldeversuche. Bitte warte kurz und versuche es erneut.",
    "ui.login.tryAgain": "Erneut versuchen",
    "ui.login.useTheStoredEmergencyPassword":
      "Das gespeicherte Notfall-Passwort verwenden",
    "ui.login.youHaveBeenSignedOut": "Du wurdest abgemeldet.",
    "ui.loginExperience.kombifyCloudSignInCompleted":
      "Die kombify-Cloud-Anmeldung wurde abgeschlossen, aber Techstack konnte keine Browsersitzung erstellen. Versuche es erneut oder wende dich an den Support.",
    "ui.managed.sizeGB": "{size} GB",
    "ui.managed.sizeGiB": "{size} GiB",
    "ui.managedCreationFlow.addNode": "Node hinzufügen",
    "ui.managedCreationFlow.failedToLoadNodeInventory":
      "Node-Inventar konnte nicht geladen werden.",
    "ui.managedCreationFlow.failedToPrepareNodeRegistration":
      "Node-Registrierung konnte nicht vorbereitet werden.",
    "ui.managedCreationFlow.newStackkitMainNode": "Neues StackKit / Haupt-Node",
    "ui.managedCreationFlow.preparingNode": "Node wird vorbereitet...",
    "ui.managedCreationFlow.proxmoxHypervisor": "Proxmox-Hypervisor",
    "ui.managedCreationFlow.selectCentronOrIonosExplicitly":
      "Wähle ausdrücklich Centron oder IONOS aus, bevor du diesem früheren StackKit-Deployment einen verwalteten Node hinzufügst.",
    "ui.managedCreationFlow.stackkitDeploymentNotFound":
      "StackKit-Deployment nicht gefunden.",
    "ui.managedCreationFlow.workerOrStorageForThis":
      "Worker oder Speicher für dieses StackKit",
    "ui.managedRuntimeRecreatePanel.thisProvisionsAndBillsA":
      "Dadurch wird eine neue Generation des verwalteten Servers bereitgestellt und abgerechnet. Techstack stellt Registrierung, Endpunkte, Guard-Nachweise und den StackKit-Rollout über den normalen Erstellungsablauf wieder her.",
    "ui.monitoring.acrossEpisodes.one": "über {count} Episode",
    "ui.monitoring.acrossEpisodes.other": "über {count} Episoden",
    "ui.monitoring.availabilityHistoryIsUnavailableOn":
      "Der Verfügbarkeitsverlauf ist in diesem Deployment nicht verfügbar.",
    "ui.monitoring.availabilityWindow": "Verfügbarkeit · {window}",
    "ui.monitoring.dailyState": "Tagesstatus",
    "ui.monitoring.dailyStateLabel": "{name}: täglicher Verbindungsstatus",
    "ui.monitoring.downFor": "{duration} ausgefallen",
    "ui.monitoring.downtimeByCause": "Ausfallzeit nach Ursache",
    "ui.monitoring.downtimeEpisodes": "Ausfall-Episoden",
    "ui.monitoring.isAnythingOnFire": "brennt irgendwo etwas",
    "ui.monitoring.lastVerifiedCountRetainedThe":
      "Letzte verifizierte Anzahl beibehalten — die Inventar-Aktualisierung ist fehlgeschlagen",
    "ui.monitoring.lifecycleConnectionAndHealthAre":
      "Lifecycle, Verbindung und Health werden gemeinsam betrachtet und nie zusammengefasst",
    "ui.monitoring.meanTimeToRecover": "Mittlere Wiederherstellungszeit",
    "ui.monitoring.monitoring": "Monitoring",
    "ui.monitoring.monitoringDataCouldNotBe":
      "Monitoring-Daten konnten nicht geladen werden.",
    "ui.monitoring.noData": "keine Daten",
    "ui.monitoring.noDowntimeInThisWindow":
      "Keine Ausfallzeit in diesem Zeitraum.",
    "ui.monitoring.noNodesHaveReportedYet":
      "Noch hat kein Node etwas gemeldet. Registriere einen Node, dann erscheint sein Status hier.",
    "ui.monitoring.noRecordedDowntimeInThis":
      "Keine erfasste Ausfallzeit in diesem Zeitraum.",
    "ui.monitoring.noServersInThisWindow": "Keine Server in diesem Zeitraum.",
    "ui.monitoring.notReporting": "{count} melden nichts",
    "ui.monitoring.notYetRecovered": "· noch nicht wiederhergestellt",
    "ui.monitoring.ongoing": "laufend",
    "ui.monitoring.openHistory": "Verlauf öffnen",
    "ui.monitoring.overObserved": "über {duration} beobachtet",
    "ui.monitoring.recoveredBy": "· wiederhergestellt um",
    "ui.monitoring.recoveredOngoingExcluded":
      "{count} wiederhergestellt · laufende ausgenommen",
    "ui.monitoring.refreshing": "Wird aktualisiert...",
    "ui.monitoring.remove": "Entfernen",
    "ui.monitoring.removeFrom": "{name} aus dem Monitoring entfernen?",
    "ui.monitoring.removeFromAria": "{name} aus dem Monitoring entfernen",
    "ui.monitoring.removing": "Wird entfernt...",
    "ui.monitoring.rightNow": "Gerade jetzt",
    "ui.monitoring.selectNodeHint":
      "wähle einen Node für seine Metriken und seinen eigenen Verlauf",
    "ui.monitoring.serverCouldNotBeRemoved":
      "Server konnte nicht entfernt werden.",
    "ui.monitoring.staleCountsAsDowntime":
      "veraltet zählt als Ausfallzeit — der Service kann nicht bestätigt werden",
    "ui.monitoring.theCanonicalServerInventoryIs":
      "Das kanonische Server-Inventar ist nicht verfügbar.",
    "ui.monitoring.thisRevokesTheBoundGuard":
      "Dadurch wird der gebundene Guard Agent widerrufen und der Node aus der aktuellen Flotte ausgeblendet. Der physische Server und dein Anbieterkonto bleiben unberührt.",
    "ui.monitoring.totalDowntime": "Gesamte Ausfallzeit",
    "ui.monitoringServer.alertsInScope": "{count} für diesen Node relevant",
    "ui.monitoringServer.allEpisodes": "Alle Episoden →",
    "ui.monitoringServer.dailyState30":
      "{name}: täglicher Verbindungsstatus über 30 Tage",
    "ui.monitoringServer.firingSince": "aktiv seit {time}",
    "ui.monitoringServer.inactive": "inaktiv",
    "ui.monitoringServer.inventoryRevision": "Inventar-Revision {revision}",
    "ui.monitoringServer.pageTitle": "{name} — Monitoring",
    "ui.monitoringServer.panelLast24h": "{title} in den letzten 24 Stunden",
    "ui.monitoringServer.peak": "Spitze {value}",
    "ui.monitoringServer.rangeQueryHint":
      'jedes Panel ist eine Bereichsabfrage, begrenzt auf node_id="{id}"',
    "ui.monitoringServer.server": "Server",
    "ui.monitoringServer.unrecorded": "nicht erfasst",
    "ui.monitoringServerId.alertRules": "Alarmregeln",
    "ui.monitoringServerId.availability30D": "Verfügbarkeit · 30 T",
    "ui.monitoringServerId.containersRunning": "Laufende Container",
    "ui.monitoringServerId.cpuUtilisation": "CPU-Auslastung",
    "ui.monitoringServerId.diskUsed": "Belegter Speicherplatz",
    "ui.monitoringServerId.guardIsRevokedImmediatelyThe":
      "Guard wird sofort widerrufen. Der physische Server bleibt bestehen.",
    "ui.monitoringServerId.memoryUsed": "Belegter Arbeitsspeicher",
    "ui.monitoringServerId.metricsLast24H": "Metriken · letzte 24 Std.",
    "ui.monitoringServerId.noAlertRulesApplyHere":
      "Hier gelten keine Alarmregeln.",
    "ui.monitoringServerId.noHistoryForThisNode":
      "Noch kein Verlauf für diesen Node.",
    "ui.monitoringServerId.noMetricsBackendAnsweredFor":
      "Für diesen Node hat kein Metrik-Backend geantwortet. Die Verfügbarkeit unten wird aus der Übergangs-Timeline abgeleitet und hängt nicht davon ab.",
    "ui.monitoringServerId.refreshing": "Wird aktualisiert…",
    "ui.monitoringServerId.removeFromView": "Aus der Ansicht entfernen",
    "ui.monitoringServerId.thisNodeSHistory": "Verlauf dieses Nodes",
    "ui.monitoringServerId.thisServerCouldNotBe":
      "Dieser Server konnte nicht geladen werden.",
    "ui.navItems.noServicesRegisteredYet": "Noch keine Services registriert.",
    "ui.nodeActions.assignToThisStackkitDeployment":
      "Diesem StackKit-Deployment zuweisen",
    "ui.nodeActions.assigning": "Wird zugewiesen...",
    "ui.nodeActions.canonicalManagedAccessIsNot":
      "Der kanonische verwaltete Zugriff ist noch nicht verfügbar.",
    "ui.nodeActions.connection": "Node-Verbindung: {state}",
    "ui.nodeRow.hideSystemServices": "System-Services ausblenden",
    "ui.nodeRow.showFewerApps": "Weniger Apps anzeigen",
    "ui.nodeRow.systemServices.one": "{count} System-Service",
    "ui.nodeRow.systemServices.other": "{count} System-Services",
    "ui.ownerState.connectYourKombifyCloudProfile":
      "Verbinde dein kombify-Cloud-Profil (mit verifizierter E-Mail), um es als Owner zu verwenden",
    "ui.ownerState.couldNotHashTheRecovery":
      "Die Wiederherstellungs-Passphrase konnte nicht gehasht werden.",
    "ui.ownerState.couldNotStartTheKombify":
      "Die kombify-Cloud-Verknüpfung konnte nicht gestartet werden.",
    "ui.ownerState.emailIsRequiredForPasswordless":
      "Für die passwortlose Authentifizierung ist eine E-Mail-Adresse erforderlich",
    "ui.ownerState.finishTheConnectionInThe":
      "Schließe die Verbindung in dem Browser ab, in dem du sie gestartet hast. Starte die Verbindung erneut.",
    "ui.ownerState.kombifyCloudLoginWasCancelled":
      "Die kombify-Cloud-Anmeldung wurde abgebrochen oder ist fehlgeschlagen.",
    "ui.ownerState.linkingTheKombifyCloudProfile":
      "Die Verknüpfung des kombify-Cloud-Profils ist fehlgeschlagen. Versuche es erneut.",
    "ui.ownerState.recoveryPassphraseMustBeAt":
      "Die Wiederherstellungs-Passphrase muss mindestens {MIN_RECOVERY_PASSPHRASE_LENGTH} Zeichen lang sein",
    "ui.ownerState.recoveryPassphrasesDoNotMatch":
      "Die Wiederherstellungs-Passphrasen stimmen nicht überein",
    "ui.ownerState.theKombifyCloudEmailIs":
      "Die E-Mail-Adresse bei kombify Cloud ist nicht verifiziert. Verifiziere sie in deinem Cloud-Konto und verknüpfe dann erneut.",
    "ui.ownerState.theKombifyCloudProfileHas":
      "Das kombify-Cloud-Profil hat keine E-Mail-Adresse.",
    "ui.ownerState.theLinkRequestExpiredStart":
      "Die Verknüpfungsanfrage ist abgelaufen. Starte die Verbindung erneut.",
    "ui.ownerState.theSelectedOwnerSourceIs":
      "Die gewählte Owner-Quelle wird nicht mehr unterstützt. Wähle einen lokalen Owner oder verknüpfe dein kombify-Cloud-Profil.",
    "ui.ownerState.thisInstanceHasNoKombify":
      "Für diese Instanz ist keine kombify-Cloud-Anmeldung konfiguriert.",
    "ui.pairing.nodeConnection": "Node-Verbindung",
    "ui.pairing.theConnectionCommandCouldNot":
      "Der Verbindungsbefehl konnte nicht vorbereitet werden. Versuche es erneut.",
    "ui.pairing.theOriginalNodeConfigurationIs":
      "Die ursprüngliche Node-Konfiguration ist nicht verfügbar. Gehe zurück zu „Node hinzufügen“, um die Verbindung vorzubereiten.",
    "ui.people.activationExpired": "Aktivierung abgelaufen",
    "ui.people.activationPending": "Aktivierung ausstehend",
    "ui.people.appCount.one": "{count} App",
    "ui.people.appCount.other": "{count} Apps",
    "ui.people.connectedClientsAppearHereOnce":
      "Verbundene Clients erscheinen hier, sobald die Geräteverwaltung von kombify Connect verfügbar ist.",
    "ui.people.deviceListUnavailable": "Geräteliste nicht verfügbar.",
    "ui.people.homelabOwner": "Homelab-Owner",
    "ui.people.householdListUnavailableTheHomelab":
      "Haushaltsliste nicht verfügbar: Der Identity Provider des Homelabs hat nicht geantwortet.",
    "ui.people.identityProviderNotReady": "Identity Provider nicht bereit",
    "ui.people.identityStatusUnavailable": "Identitätsstatus nicht verfügbar",
    "ui.people.member": "Mitglied",
    "ui.people.notInvitedYet": "Noch nicht eingeladen",
    "ui.people.ownerAndHouseholdAppearOnce":
      "Owner und Haushalt erscheinen, sobald der Identity Provider des Homelabs (Pocket ID oder TinyAuth) über den Guard Bericht erstattet.",
    "ui.people.plannedInTheWizard": "Im Assistenten geplant",
    "ui.people.plannedMember": "Geplantes Mitglied",
    "ui.people.signedInToTheHomelab": "Am Homelab angemeldet",
    "ui.peoplePanel.canManageTheHomelab": "Kann das Homelab verwalten",
    "ui.peoplePanel.device": "Gerät",
    "ui.peoplePanel.household": "Haushalt",
    "ui.peoplePanel.householdDevicesAreNotTracked":
      "Haushaltsgeräte werden von kombify Connect nicht erfasst.",
    "ui.peoplePanel.householdMember": "Haushaltsmitglied",
    "ui.peoplePanel.kombifyClients": "kombify-Clients",
    "ui.peoplePanel.noHouseholdMembersYetInvite":
      "Noch keine Haushaltsmitglieder. Lade sie über die Identitätseinrichtung des Homelabs ein.",
    "ui.peoplePanel.noKombifyClientIsLinked":
      "Noch kein kombify-Client verknüpft. Installiere den Techstack-Client, Companion oder Workbench, damit er hier erscheint.",
    "ui.peoplePanel.nobodyInThisFilter": "Niemand in diesem Filter.",
    "ui.peoplePanel.ownerDevicesCanManageThe":
      "Owner-Geräte können das Homelab verwalten; Haushaltsmitglieder nutzen nur dessen Apps.",
    "ui.peoplePanel.person": "Person",
    "ui.peoplePanel.platform": "Plattform",
    "ui.peoplePanel.usesTheHomelabSApps": "Nutzt die Apps des Homelabs",
    "ui.postMessageBridge.authTokenRequestTimedOut":
      "Zeitüberschreitung bei der Anfrage des Auth-Tokens",
    "ui.postMessageBridge.authenticationFailed":
      "Authentifizierung fehlgeschlagen",
    "ui.postMessageBridge.bridgeDestroyed": "Bridge zerstört",
    "ui.postMessageBridge.gatewayAuthenticationFailed":
      "Gateway-Authentifizierung fehlgeschlagen",
    "ui.postMessageBridge.gatewayTokenRequestTimedOut":
      "Zeitüberschreitung bei der Anfrage des Gateway-Tokens",
    "ui.postMessageBridge.kombifyAIDidNotConfirm":
      "kombify AI hat die Support-Sitzung nicht rechtzeitig bestätigt.",
    "ui.presets.aCalmOverviewOneBand":
      "Ein ruhiger Überblick: ein Band pro Node mit seiner 24-Stunden-Kurve.",
    "ui.presets.complete": "Vollständig",
    "ui.presets.nodeRowsWithTheirApps":
      "Node-Zeilen mit ihren Apps, Geräten und Personen daneben.",
    "ui.providerErrors.aStackKitWasSelectedE":
      "Es wurde ein StackKit ausgewählt (z. B. basement-kit oder cloud-kit), aber die StackKit-Dateien sind auf dem Server nicht verfügbar.",
    "ui.providerErrors.anUnknownErrorOccurred":
      "Ein unbekannter Fehler ist aufgetreten.",
    "ui.providerErrors.baseStackkitNotFound": "Basis-StackKit nicht gefunden",
    "ui.providerErrors.baseSubdomainLimitReached":
      "Limit für Basis-Subdomains erreicht",
    "ui.providerErrors.bootstrapManagedRuntimeTarget":
      "Bootstrap des verwalteten Runtime-Ziels",
    "ui.providerErrors.checkCloudInitDockerStatus":
      "Prüfe cloud-init, den Docker-Status und die SSH-Erreichbarkeit auf dem Managed-Runtime-Server",
    "ui.providerErrors.checkPortConflictsInThe":
      "Prüfe die Fehlermeldung auf Portkonflikte",
    "ui.providerErrors.checkThatCUEIsInstalled":
      "Prüfe, ob CUE korrekt installiert ist",
    "ui.providerErrors.checkThatPort5260Is":
      "Prüfe, ob Port 5260 erreichbar ist",
    "ui.providerErrors.checkThatStackKitFilesAre":
      "Prüfe, ob die StackKit-Dateien im konfigurierten StackKits-Checkout vorhanden sind",
    "ui.providerErrors.checkThatThePocketBaseDatabase":
      "Prüfe, ob die PocketBase-Datenbank läuft",
    "ui.providerErrors.checkThatTheStackName":
      "Prüfe, ob der Stack-Name gültig ist (nur Buchstaben, Zahlen und Bindestriche)",
    "ui.providerErrors.checkThatTheSubmittedValues":
      "Prüfe, ob die übermittelten Werte plausibel sind",
    "ui.providerErrors.checkTheBrowserConsoleFor":
      "Prüfe die Browserkonsole auf JavaScript-Fehler",
    "ui.providerErrors.checkTheErrorDetailsFor":
      "Prüfe die Fehlerdetails auf kombify.me-Registrierung, Kontingent oder StackKits-CLI-Ausgabe",
    "ui.providerErrors.checkTheErrorDetailsFor2":
      "Prüfe die Fehlerdetails auf Backend-Fehler, Ziel-Bootstrap und Runtime-Diagnosen",
    "ui.providerErrors.checkTheLifecycleReceiptAnd":
      "Prüfe den Lifecycle-Beleg und den Status der automatischen Bereinigung; starte keinen weiteren Node, bevor das endgültige Fehlen bestätigt ist",
    "ui.providerErrors.checkTheProviderErrorCode":
      "Prüfe den Fehlercode des Anbieters in den Fehlerdetails",
    "ui.providerErrors.checkTheProviderPortalTo":
      "Prüfe im Anbieter-Portal, ob der Server noch startet oder neu gestartet wurde",
    "ui.providerErrors.checkTheRuntimeActionResponse":
      "Prüfe die Runtime-Action-Antwort auf stackkit_outputs.identity.owner.username",
    "ui.providerErrors.checkTheRuntimeActionResponse2":
      "Prüfe die Runtime-Action-Antwort auf stackkit_outputs.login_gateway.url",
    "ui.providerErrors.checkTheRuntimeActionResponse3":
      "Prüfe die Runtime-Action-Antwort auf stackkit_outputs.identity.recovery",
    "ui.providerErrors.checkTheRuntimeLogsFor":
      "Prüfe die Runtime-Logs für denselben Stack, Job, dasselbe Lease und denselben Anbieter",
    "ui.providerErrors.checkTheServerLogsDocker":
      "Prüfe die Server-Logs: 'docker compose logs techstack'",
    "ui.providerErrors.checkTheVMLeaseEnrollment":
      "Prüfe die Registrierungsereignisse des VM-Leases und Sentry auf den Anbieterfehler",
    "ui.providerErrors.checkWhetherCentronOrIONOS":
      "Prüfe, ob Centron oder IONOS den Server tatsächlich erstellt hat",
    "ui.providerErrors.checkWhetherTheServerStill":
      "Prüfe, ob der Server bei Centron oder IONOS noch existiert",
    "ui.providerErrors.checkYourInternetConnection":
      "Prüfe deine Internetverbindung",
    "ui.providerErrors.configurationCouldNotBeValidated":
      "Die Konfiguration konnte nicht validiert werden",
    "ui.providerErrors.createAGitHubIssueWith":
      "Erstelle ein GitHub-Issue mit den Schritten zur Reproduktion",
    "ui.providerErrors.databaseError": "Datenbankfehler",
    "ui.providerErrors.disableConflictingServices":
      "Deaktiviere in Konflikt stehende Services",
    "ui.providerErrors.doNotCreateAnotherProvider":
      "Erstelle keine weitere Anbieter-VM; verwende den bestehenden Stack und das bestehende Lease für den nächsten Rollout-Versuch weiter",
    "ui.providerErrors.doNotCreateAnotherProvider2":
      "Erstelle keine weitere Anbieter-VM, bis der bestehende Job ein Diagnose-Artefakt oder einen klaren Überspringungsgrund hat",
    "ui.providerErrors.doNotRetryAdditionalNode":
      "Wiederhole „Zusätzlicher Node“ nicht gegen ein v1-Homelab",
    "ui.providerErrors.errorCode": "Fehlercode: {code}",
    "ui.providerErrors.forAuthenticationErrorsMakeSure":
      "Stelle bei Authentifizierungsfehlern sicher, dass die Passwörter übereinstimmen",
    "ui.providerErrors.forCustomStackKitsValidateThe":
      "Bei eigenen StackKits: Validiere die CUE-Syntax",
    "ui.providerErrors.forMonitoringVictoriaMetricsRetentionRequires":
      "Für das Monitoring: Die VictoriaMetrics-Aufbewahrung erfordert persistenten Speicher",
    "ui.providerErrors.forPersistentErrorsCreateA":
      "Bei anhaltenden Fehlern: Erstelle ein GitHub-Issue mit den Logs",
    "ui.providerErrors.forVPNServicesOnlyOne":
      "Bei VPN-Services: Es kann jeweils nur ein VPN-Anbieter aktiv sein",
    "ui.providerErrors.foundANewKitFor":
      "Gründe ein neues Kit für diesen Server, statt dem bestehenden v1-Deployment beizutreten",
    "ui.providerErrors.ifProviderSupportIsRequired":
      "Falls der Anbieter-Support nötig ist, gib den Fehlercode aus den Details an",
    "ui.providerErrors.ifRunningTheBinaryOutside":
      "Wenn du das Binary außerhalb des Repos ausführst: Setze TECHSTACK_STACKKITS_DIR auf einen veröffentlichten StackKits-Checkout",
    "ui.providerErrors.ifThisIsANew":
      "Wenn dies ein neues Homelab ist, gründe ein Kit, statt einen Node hinzuzufügen",
    "ui.providerErrors.ifThisIsANew2":
      "Wenn dies ein neues Deployment ist, wiederhole den Assistenten, damit Techstack eine stackkit/v2alpha1-Spezifikation projizieren kann",
    "ui.providerErrors.joinRequiresAnArchitectureV2":
      "Der Beitritt erfordert eine Architektur-v2",
    "ui.providerErrors.joiningASecondController":
      "Beitritt eines zweiten Controllers",
    "ui.providerErrors.kitDirectoryNotFound": "Kit-Verzeichnis nicht gefunden",
    "ui.providerErrors.kombifyMeRegistrationFailed":
      "kombify.me-Registrierung fehlgeschlagen",
    "ui.providerErrors.makeSureAStackKitWas":
      "Stelle sicher, dass ein StackKit ausgewählt oder erkannt wurde",
    "ui.providerErrors.makeSureTheKombifyTechstack":
      "Stelle sicher, dass der kombify-Techstack-Server läuft",
    "ui.providerErrors.managedRuntimeCouldNotBe":
      "Managed Runtime konnte nicht vorbereitet werden",
    "ui.providerErrors.managedRuntimeCouldNotBe2":
      "Managed Runtime konnte nicht erstellt werden",
    "ui.providerErrors.managedRuntimeIsNotReady":
      "Managed Runtime ist noch nicht bereit",
    "ui.providerErrors.managedRuntimeTargetBootstrapFailed":
      "Bootstrap des verwalteten Runtime-Ziels fehlgeschlagen",
    "ui.providerErrors.nameIsAlreadyInUse": "Name wird bereits verwendet",
    "ui.providerErrors.networkErrorWhileSaving":
      "Netzwerkfehler beim Speichern",
    "ui.providerErrors.nextStepCheckLimitsQuota":
      "Nächster Schritt: Prüfe im Anbieter-Portal Limits, Kontingent und laufende Server und gib dann die benötigten Ressourcen frei.",
    "ui.providerErrors.nextStepCheckTheLifecycle":
      "Nächster Schritt: Prüfe den Lifecycle-Beleg und den Status der automatischen Bereinigung. Wiederhole erst, wenn das endgültige Fehlen bestätigt ist.",
    "ui.providerErrors.nextStepCheckTheProvider":
      "Nächster Schritt: Prüfe das Anbieter-Portal. Falls der Fehler weiterbesteht, sende den Fehlercode an den Anbieter-Support.",
    "ui.providerErrors.nextStepWaitForThe":
      "Nächster Schritt: Warte, bis die Erstellungs-/Lösch-Abkühlzeit des Anbieters abgelaufen ist, und wiederhole dann die Erstellung.",
    "ui.providerErrors.noCompatibleStackKitIsAvailable":
      "Für die gewählte Konfiguration ist kein kompatibles StackKit verfügbar.",
    "ui.providerErrors.noMatchingStackKitFound":
      "Kein passendes StackKit gefunden",
    "ui.providerErrors.noSubdomainprefixIsConfigured":
      "Es ist kein subdomainprefix konfiguriert",
    "ui.providerErrors.openTheLatestDestroyJob":
      "Öffne den neuesten Destroy-Job für den eigenen Fehler des Anbieters",
    "ui.providerErrors.providerMessage": "Meldung des Anbieters: {summary}",
    "ui.providerErrors.reloadThePageAndTry":
      "Lade die Seite neu und versuche es erneut",
    "ui.providerErrors.resolveTheKombifyMeOr":
      "Behebe das kombify.me- oder StackKits-Hindernis und wiederhole dann nur den StackKit-Rollout",
    "ui.providerErrors.retryAdditionalNodeAFoundation":
      "Wiederhole „Zusätzlicher Node“. Eine Foundation-Rolle tritt als Worker einem bestehenden Kit bei",
    "ui.providerErrors.retryCreationOnlyAfterSSH":
      "Wiederhole die Erstellung erst, wenn SSH stabil ist und Docker fehlerfrei startet",
    "ui.providerErrors.retryCreationOnlyAfterThe":
      "Wiederhole die Erstellung erst, wenn das Lease runtime_ssh_host oder runtime_public_ip meldet",
    "ui.providerErrors.retryOnlyAfterTheStackKit":
      "Wiederhole erst, wenn der Runtime-Action-Vertrag des StackKits diese Ausgaben liefert",
    "ui.providerErrors.retryTheRolloutOnlyAfter":
      "Wiederhole den Rollout erst, wenn SSH, Docker und die StackKits Runtime Action auf dem Zielserver stabil sind",
    "ui.providerErrors.reviewTheLogsWithDocker":
      "Sieh dir die Logs mit 'docker compose logs techstack' an",
    "ui.providerErrors.reviewTheServerLogs": "Sieh dir die Server-Logs an",
    "ui.providerErrors.runtimeActionStackkitRollout":
      "Runtime-Action stackkit_rollout",
    "ui.providerErrors.serviceConflictDetected": "Service-Konflikt erkannt",
    "ui.providerErrors.stackkitArtifactGenerationFailed":
      "Erzeugung der StackKit-Artefakte fehlgeschlagen",
    "ui.providerErrors.stackkitArtifactsCouldNotBe":
      "StackKit-Artefakte konnten nicht erzeugt werden",
    "ui.providerErrors.stackkitCliGenerateFailed":
      "StackKit-CLI-generate fehlgeschlagen",
    "ui.providerErrors.stackkitFilesMissing": "StackKit-Dateien fehlen",
    "ui.providerErrors.stackkitRolloutCouldNotBe":
      "StackKit-Rollout konnte nicht angewendet werden",
    "ui.providerErrors.stackkitsArtifactGenerationFailed":
      "Erzeugung der StackKits-Artefakte fehlgeschlagen",
    "ui.providerErrors.stackkitsCliGenerateFailed":
      "StackKits-CLI-generate fehlgeschlagen",
    "ui.providerErrors.stackkitsCouldNotApply":
      "StackKits konnten nicht angewendet werden",
    "ui.providerErrors.stackkitsOnlyAcceptsAnArchitecture":
      "StackKits akzeptiert nur eine StackSpec der Architektur v2. Ein v1-Dokument oder v2-Felder wie useCases in einem v1-Dokument können nicht validiert oder verbunden werden.",
    "ui.providerErrors.switchToADifferentAccess":
      "Wechsle zu einem anderen Zugriffsmodus (Zuhause/Überall)",
    "ui.providerErrors.technicalDetails": "Technische Details:",
    "ui.providerErrors.theAdditionalNodeIntentCould":
      "Die Absicht „Zusätzlicher Node“ konnte nicht auf die Kit-Spezifikation der Architektur v2 projiziert werden.",
    "ui.providerErrors.theCloudProviderRejectedOr":
      "Der Cloud-Anbieter hat die VM-Erstellung abgelehnt oder nicht abgeschlossen. Der Anbieterfehler steht in den Details unten.",
    "ui.providerErrors.theCloudProviderRejectedServer":
      "Der Cloud-Anbieter hat die Server-Erstellung abgelehnt.",
    "ui.providerErrors.theConfigurationCouldNotBe":
      "Die Konfiguration konnte nicht in der Datenbank gespeichert werden.",
    "ui.providerErrors.theConfigurationCouldNotBe2":
      "Die Konfiguration konnte nicht in eine gültige Deployment-Spezifikation umgewandelt werden.",
    "ui.providerErrors.theKombifyTechstackServerCould":
      "Der kombify-Techstack-Server war nicht erreichbar.",
    "ui.providerErrors.theManagedVMWasPrepared":
      "Die verwaltete VM wurde vorbereitet, aber StackKits konnte die Rollout-Artefakte oder die kombify.me-Routing-Daten nicht erzeugen.",
    "ui.providerErrors.theRolloutDidNotReturn":
      "Der Rollout hat die Owner-Anmeldung, das Login-Gateway und die Wiederherstellungs-Ausgaben nicht zurückgegeben, die zur Nutzung des Stacks nötig sind.",
    "ui.providerErrors.theSelectedServicesHaveConflicting":
      "Die gewählten Services haben widersprüchliche Anforderungen.",
    "ui.providerErrors.theSubmittedConfigurationContainsInvalid":
      "Die übermittelte Konfiguration enthält ungültige oder fehlende Werte.",
    "ui.providerErrors.theTeardownStoppedBecauseTechstack":
      "Der Abbau wurde gestoppt, weil Techstack die Anfrage keinem maßgeblichen Anbieter-Lease zuordnen konnte. Es wurde nichts zwangsweise entfernt, daher können Ressourcen beim Anbieter noch existieren.",
    "ui.providerErrors.theVMIsReachableBut":
      "Die VM ist erreichbar, aber Techstack konnte Docker oder die Bootstrap-Basis auf dem Managed-Runtime-Server nicht zuverlässig vorbereiten.",
    "ui.providerErrors.theVMLeaseHasNot":
      "Das VM-Lease hat weder einen SSH-Host noch eine öffentliche IP gemeldet. Die Erstellung wurde gestoppt, damit der Vorgang nicht endlos in der Bereitstellung bleibt.",
    "ui.providerErrors.theVMWasPreparedBut":
      "Die VM wurde vorbereitet, aber die StackKits Runtime Action konnte den gewählten StackKit-Rollout nicht anwenden.",
    "ui.providerErrors.thisDeploymentCouldNotBe":
      "Dieses Deployment konnte nicht außer Betrieb genommen werden",
    "ui.providerErrors.thisDeploymentIsNotArchitecture":
      "Dieses Deployment ist nicht Architektur v2",
    "ui.providerErrors.thisNodeCouldNotBe":
      "Dieser Node konnte nicht hinzugefügt werden",
    "ui.providerErrors.thisNodeCouldNotBe2":
      "dieser Node konnte nicht hinzugefügt werden",
    "ui.providerErrors.tooManyRecentCreateAnd":
      "zu viele kürzliche Erstell- und Löschvorgänge",
    "ui.providerErrors.tooManyRecentCreateDelete":
      "zu viele kürzliche Erstell-/Löschvorgänge",
    "ui.providerErrors.tryRestartingTheKombifyTechstack":
      "Versuche, den kombify-Techstack-Server neu zu starten",
    "ui.providerErrors.trySelectingFewerServices":
      "Versuche, weniger Services auszuwählen",
    "ui.providerErrors.unexpectedError": "Unerwarteter Fehler",
    "ui.providerErrors.unifierProcessingError":
      "Verarbeitungsfehler im Unifier",
    "ui.providerErrors.useTheServerSForce":
      "Nutze die erzwungene Außerbetriebnahme des Servers erst, nachdem du bestätigt hast, dass die Anbieter-Ressourcen verschwunden sind",
    "ui.providerErrors.v1StackspecCannotCarry":
      "Eine v1-StackSpec kann Folgendes nicht enthalten:",
    "ui.providerErrors.validateTheStackSpecYaml":
      "Validiere die stack-spec.yaml manuell mit dem StackKits-Validator",
    "ui.providerErrors.verifyTheStackKitDirectoryExists":
      "Prüfe, ob das StackKit-Verzeichnis auf dem Server existiert (z. B. /app/stackkits/basement-kit oder /app/stackkits/cloud-kit)",
    "ui.providerErrors.verifyWritePermissionsForThe":
      "Prüfe die Schreibrechte für das Verzeichnis pb_data/",
    "ui.providerErrors.waitForRateLimitsOr":
      "Warte, bis Ratenbegrenzungen oder die Drosselung beim Erstellen/Löschen aufgehoben sind, bevor du es erneut versuchst",
    "ui.providerErrors.withDockerEnsureTheVolume":
      "Mit Docker: Stelle sicher, dass das Volume korrekt eingebunden ist",
    "ui.providerErrors.withDockerRebuildTheImage":
      "Mit Docker: Baue das Image neu (stellt sicher, dass der angepinnte StackKits-Checkout vorhanden ist)",
    "ui.providerErrors.withDockerUseDockerPs":
      "Mit Docker: Prüfe mit 'docker ps', ob alle Container laufen",
    "ui.providerErrors.withoutExitStatusOrExit":
      "ohne Exit-Status oder Exit-Signal",
    "ui.recreate.confirmTitle": "{name} neu erstellen?",
    "ui.requirements.1KombifyCloudServerIs":
      "1 kombify-Cloud-Server wird automatisch bereitgestellt",
    "ui.requirements.aCloudServerIsRequired":
      "Für den externen Zugriff ist ein Cloud-Server erforderlich",
    "ui.requirements.aKombifyCloudServerIs":
      "Ein kombify-Cloud-Server wird über das Abonnement automatisch bereitgestellt",
    "ui.requirements.aLocalServerIsRequired":
      "Für Homelab-Services ist ein lokaler Server erforderlich",
    "ui.requirements.anExistingRemoteServerIs":
      "Ein bestehender Remote-Server wird über SSH verbunden",
    "ui.requirements.atLeast1ExistingServer": "Mindestens 1 bestehender Server",
    "ui.requirements.atLeast1UserOwned": "Mindestens 1 eigener Server",
    "ui.requirements.atLeastCloudServer":
      "Mindestens {minCloudServers} Cloud-Server",
    "ui.requirements.atLeastCloudServerAnd":
      "Mindestens {minCloudServers} Cloud-Server und {minLocalServers} lokaler Server",
    "ui.requirements.atLeastLocalServer":
      "Mindestens {minLocalServers} lokaler Server",
    "ui.requirements.completeSetupAtLeast1":
      "Vollständige Einrichtung: Mindestens 1 leistungsfähiger Server",
    "ui.requirements.completeSetupKombifyCloudServer":
      "Vollständige Einrichtung: kombify-Cloud-Server mit ausreichenden Ressourcen",
    "ui.requirements.dockerContainer": "Docker-Container",
    "ui.requirements.homeAssistantOSCanRun":
      "Home Assistant OS kann in einer separaten Appliance-VM auf demselben Hypervisor laufen.",
    "ui.requirements.hybridSetupCloudAndLocal":
      "Hybride Einrichtung: Cloud- und lokale Server sind verbunden",
    "ui.requirements.linuxRecommendedInstallsThePersistent":
      "Linux (empfohlen) — installiert den dauerhaften ausgehenden Guard über die Core-/API-URL (/install.sh). Wenn du auf einem anderen Host/einer anderen VM ausführst, ersetze localhost durch die erreichbare IP/Domain deines kombify-Techstack-Servers.",
    "ui.requirements.manualInstallationAfterBinaryDownload":
      "Manuelle Installation (nach dem Binary-Download)",
    "ui.requirements.techstackPreparesTheVMThen":
      "Techstack bereitet die VM vor und setzt dann die Standard-StackKits-Installation fort.",
    "ui.requirements.theInstallationCommandRunsOn":
      "Der Installationsbefehl läuft auf deinem eigenen Server oder Gerät",
    "ui.requirements.ubuntuGuestOnYourProxmox":
      "Ubuntu-Gast auf deinem Proxmox-Hypervisor",
    "ui.rotation.lastEvery": "Zuletzt: {last} • Alle {days} T",
    "ui.rotation.needAttention": "{count} erfordern Aufmerksamkeit",
    "ui.rotationReminders.daysAgo.one": "vor {count} Tag",
    "ui.rotationReminders.daysAgo.other": "vor {count} Tagen",
    "ui.rotationReminders.monthsAgo.one": "vor {count} Monat",
    "ui.rotationReminders.monthsAgo.other": "vor {count} Monaten",
    "ui.rotationReminders.never": "Nie",
    "ui.rotationReminders.overdue": "Überfällig",
    "ui.rotationReminders.recommendedRotationApiKeys90d":
      "Empfohlene Rotation: API-Schlüssel (90 T), Passwörter (180 T), OAuth-Tokens (30 T), Zertifikate (365 T)",
    "ui.rotationReminders.setupNeeded": "Einrichtung erforderlich",
    "ui.rotationReminders.today": "Heute",
    "ui.rotationReminders.yesterday": "Gestern",
    "ui.sSHKeyGenerator.addThisToSshAuthorized":
      "Füge das auf deinen Servern zu ~/.ssh/authorized_keys hinzu",
    "ui.sSHKeyGenerator.algorithm": "Algorithmus",
    "ui.sSHKeyGenerator.algorithm2": "Algorithmus:",
    "ui.sSHKeyGenerator.close": "Schließen",
    "ui.sSHKeyGenerator.downloadAndSecurelyStoreYour":
      "Lade deinen privaten Schlüssel jetzt herunter und bewahre ihn sicher auf. Bei Verlust kann er nicht wiederhergestellt werden. Gib deinen privaten Schlüssel niemals weiter.",
    "ui.sSHKeyGenerator.downloadPub": ".pub herunterladen",
    "ui.sSHKeyGenerator.eGProductionServerGithub":
      "z. B. Produktionsserver, GitHub-Deploy",
    "ui.sSHKeyGenerator.ed25519IsRecommendedForMost":
      "Ed25519 wird für die meisten Anwendungsfälle empfohlen. Nutze RSA nur, wenn du Kompatibilität mit älteren Systemen brauchst.",
    "ui.sSHKeyGenerator.failedToSaveKey":
      "Schlüssel konnte nicht gespeichert werden",
    "ui.sSHKeyGenerator.fingerprint": "Fingerabdruck:",
    "ui.sSHKeyGenerator.generate": "Generieren",
    "ui.sSHKeyGenerator.generating": "Wird generiert...",
    "ui.sSHKeyGenerator.keyGenerated": "Schlüssel generiert",
    "ui.sSHKeyGenerator.keyGenerationFailed":
      "Schlüsselgenerierung fehlgeschlagen",
    "ui.sSHKeyGenerator.keyName": "Schlüsselname",
    "ui.sSHKeyGenerator.keyPairGeneratedSuccessfully":
      "Schlüsselpaar erfolgreich generiert",
    "ui.sSHKeyGenerator.maximumCompatibilityStronger":
      "Maximale Kompatibilität, stärker",
    "ui.sSHKeyGenerator.modernFastSecureRecommended":
      "Modern, schnell, sicher (empfohlen)",
    "ui.sSHKeyGenerator.name": "Name:",
    "ui.sSHKeyGenerator.pleaseEnterANameFor":
      "Bitte gib einen Namen für diesen Schlüssel ein",
    "ui.sSHKeyGenerator.privateKey": "Privater Schlüssel",
    "ui.sSHKeyGenerator.publicKey": "Öffentlicher Schlüssel",
    "ui.sSHKeyGenerator.saveToWallet": "In Wallet speichern",
    "ui.sSHKeyGenerator.sshKeyGenerationRequiresHttps":
      "Die SSH-Schlüsselgenerierung erfordert HTTPS und einen modernen Browser mit Unterstützung für die Web Crypto API.",
    "ui.sSHKeyGenerator.tip": "Tipp:",
    "ui.sSHKeyGenerator.wideCompatibilityStandardStrength":
      "Breite Kompatibilität, Standardstärke",
    "ui.serverAccess.installKeyConfirm":
      'Öffentlichen Schlüssel "{key}" einmalig auf {server} installieren? Der private Schlüssel verlässt die Wallet nie.',
    "ui.serverAccess.sshKey": "SSH-Schlüssel",
    "ui.serverAccessActions.addOneInTheWallet":
      "Füge in der Wallet einen hinzu",
    "ui.serverAccessActions.authorizeOneOfYourPublic":
      "Autorisiere zuerst einen deiner öffentlichen Schlüssel auf diesem Node.",
    "ui.serverAccessActions.authorizeSshKey": "SSH-Schlüssel autorisieren",
    "ui.serverAccessActions.copySshCommand": "SSH-Befehl kopieren",
    "ui.serverAccessActions.installPublicKey":
      "Öffentlichen Schlüssel installieren",
    "ui.serverAccessActions.noSshKeyInThe":
      "Noch kein SSH-Schlüssel in der Wallet.",
    "ui.serverAccessActions.openTerminal": "Terminal öffnen",
    "ui.serverAccessActions.sshCommandIsUnavailable":
      "SSH-Befehl nicht verfügbar",
    "ui.serverAccessActions.terminalAccessIsUnavailable":
      "Terminalzugriff nicht verfügbar",
    "ui.serverAccessActions.thisCommandRequiresOneOf":
      "Dieser Befehl erfordert einen deiner zuvor autorisierten SSH-Schlüssel.",
    "ui.serverAccessActions.walletPublicKey": "Öffentlicher Wallet-Schlüssel",
    "ui.serverCard.archUnknown": "Architektur unbekannt",
    "ui.serverCard.osUnknown": "Betriebssystem unbekannt",
    "ui.serverCardAdapter.anotherNodeActionIsRunning":
      "Eine andere Node-Aktion läuft gerade",
    "ui.serverDetail.actionNotStarted":
      "{action} konnte nicht gestartet werden.",
    "ui.serverDetail.actionsTarget":
      "Diese Aktionen richten sich direkt an {name}. Die verfügbaren Steuerelemente richten sich nach dem aktuellen Verbindungs- und StackKit-Status; ein Server- oder Lifecycle-Status muss nicht ausgewählt werden.",
    "ui.serverDetail.confirmActionFor": "„{action}“ für {name} bestätigen",
    "ui.serverDetail.confirmDecommissionFor":
      "Außerbetriebnahme von {name} bestätigen",
    "ui.serverDetail.confirmDetachFor": "Trennen von {name} bestätigen",
    "ui.serverDetail.confirmProviderRemoval":
      "Entfernung von {name} beim Anbieter bestätigen",
    "ui.serverDetail.connectionState": "Serververbindung: {state}",
    "ui.serverDetail.decommissionIntro":
      "Fordere die kontrollierte Entfernung von {slot} an. Techstack behält den Custody-Eintrag des Anbieters, bis die Abwesenheit verifiziert ist. Diese Aktion ist bewusst nur hier verfügbar.",
    "ui.serverDetail.detachIntro":
      "Widerrufe den genauen Guard Agent für {slot} und entferne diese Anbindung aus dem aktuellen Inventar. Techstack behält den abschließenden Audit-Beleg und führt keinen API-Aufruf beim Anbieter aus.",
    "ui.serverDetail.evidence": "Nachweis: {sources}",
    "ui.serverDetail.healthSource":
      "Quelle: {source}. Monitoring-Backend: {backend}.",
    "ui.serverDetail.jobAccepted":
      "Job {id} wurde für diesen Server angenommen.",
    "ui.serverDetail.jobLabel": "Job {id}",
    "ui.serverDetail.observedAt": "Beobachtet {time}",
    "ui.serverDetail.sourceNotReported": "Quelle nicht gemeldet",
    "ui.serverLifecycleActions.applyPlan": "Plan anwenden",
    "ui.serverLifecycleActions.applyThePreparedStackKitPlan":
      "Wende den vorbereiteten StackKit-Plan auf diesen Server an.",
    "ui.serverLifecycleActions.compareTheRunningServerWith":
      "Vergleiche den laufenden Server mit seinem gewünschten StackKit-Zustand.",
    "ui.serverLifecycleActions.detectDrift": "Drift erkennen",
    "ui.serverLifecycleActions.planChanges": "Änderungen planen",
    "ui.serverLifecycleActions.previewTheNextStackKitChange":
      "Zeige eine Vorschau der nächsten StackKit-Änderung für diesen Server.",
    "ui.serverLifecycleActions.reconcileDrift": "Drift beheben",
    "ui.serverLifecycleActions.restoreTheDesiredStackKitState":
      "Stelle den gewünschten StackKit-Zustand auf diesem Server wieder her.",
    "ui.serverLifecycleActions.upgradeThroughThePublishedStackKits":
      "Aktualisiere über den veröffentlichten StackKits-Release-Kanal.",
    "ui.serverLifecycleActions.upgradeToLatest":
      "Auf neueste Version aktualisieren",
    "ui.serverLifecycleActions.verifyInstallation": "Installation verifizieren",
    "ui.serverLifecycleActions.verifyReleaseReceiptOwnerBinding":
      "Verifiziere Release-Beleg, Owner-Bindung und Runtime-Zustand.",
    "ui.serverList.inventoryUnavailable": "Inventar nicht verfügbar",
    "ui.serverList.theCanonicalInventoryIsUnavailable":
      "Das kanonische Inventar ist nicht verfügbar. Es wird nur Telemetrie angezeigt, die aktuell autorisiert ist.",
    "ui.serverOutcome.continueOnConnected":
      "StackKit auf verbundenem Node fortsetzen",
    "ui.serverOutcome.continuePrepOnConnected":
      "StackKit-Vorbereitung auf verbundenem Node fortsetzen",
    "ui.serverOutcome.restartRollout": "Rollout neu starten",
    "ui.serverOutcome.retrySsh": "SSH-Verbindung erneut versuchen",
    "ui.serverProvisioningStep.cloudKitRollout": "Cloud-Kit-Rollout",
    "ui.serverRegistry.addOneToHomelab": "füge eines zu diesem Homelab hinzu",
    "ui.serverRegistry.joinSurface":
      "Dieser Node tritt dem bestehenden StackKit-Deployment bei, das bereits seinen Foundation Node hat. Ein zweiter Haupt-Node betreibt sein eigenes StackKit-Deployment — {slot}.",
    "ui.serverRegistryPanel.everyRegisteredServerIsBound":
      "Jeder registrierte Server ist an genau eine konkrete StackKit-Grundlage gebunden.",
    "ui.serverRegistryPanel.foundationNodeIsTheProduct":
      "Foundation Node ist die Produktbezeichnung für den ersten bzw. zentralen Server.",
    "ui.serverRegistryPanel.optionalServices": "Optionale Services",
    "ui.serverRegistryPanel.serverRole": "Serverrolle",
    "ui.serverRegistryPanel.stackkitFoundation": "StackKit-Grundlage",
    "ui.serverRegistryPanel.theseSelectionsFeedTheSame":
      "Diese Auswahl speist denselben Service-Registry-Vertrag, den auch das Service-Management verwendet.",
    "ui.serverRegistryPanel.thisNodeJoinsTheExisting":
      "Dieser Node tritt dem bestehenden StackKit bei. Die Grundlage ist bereits gebunden.",
    "ui.serverTerminalModal.invalidTerminalStreamResponse":
      "Ungültige Antwort des Terminal-Streams",
    "ui.serverTerminalModal.terminalConnectionFailed":
      "Terminalverbindung fehlgeschlagen",
    "ui.serviceCardAdapter.anotherGovernedActionIsRunning":
      "Eine andere gesteuerte Aktion läuft gerade",
    "ui.serviceCardAdapter.archivedSourceService":
      "Archivierter Quell-Service.",
    "ui.serviceCardAdapter.endpointIsReachableButProtected":
      "Der Endpunkt ist erreichbar, aber der Health-Status des geschützten Services ist nicht verifiziert.",
    "ui.serviceCardAdapter.locked": "Gesperrt",
    "ui.serviceCardAdapter.lockedBy": "Gesperrt von {actor}",
    "ui.serviceCardAdapter.noCurrentRuntimeObservationIs":
      "Keine aktuelle Runtime-Beobachtung verfügbar.",
    "ui.serviceCardAdapter.observedOnlyAdoptToManage":
      "Nur beobachtet — übernimm ihn, um den Lifecycle zu verwalten.",
    "ui.serviceCardAdapter.serviceReportedAnError":
      "Der Service hat einen Fehler gemeldet.",
    "ui.serviceCardAdapter.waitingForADockerHealth":
      "Warte auf einen Docker-Health- oder Endpunkt-Check.",
    "ui.serviceCardAdapter.waitingForPlacement": "Warte auf Platzierung.",
    "ui.serviceDiscovery.adding": "Wird hinzugefügt...",
    "ui.serviceDiscovery.deselectAll": "Alle abwählen",
    "ui.serviceDiscovery.discoveryFailed": "Erkennung fehlgeschlagen",
    "ui.serviceDiscovery.failedToAddCredentials":
      "Zugangsdaten konnten nicht hinzugefügt werden",
    "ui.serviceDiscovery.noUrl": "Keine URL",
    "ui.serviceDiscovery.selectAll": "Alle auswählen",
    "ui.serviceList.servicesByRuntimeTarget": "Services nach Runtime-Ziel",
    "ui.serviceRegistry.selected.one": "{count} Service ausgewählt",
    "ui.serviceRegistry.selected.other": "{count} Services ausgewählt",
    "ui.serviceSheet.node": "Node: {node}",
    "ui.serviceSheet.notPlaced": "nicht platziert",
    "ui.serviceSheet.ownership": "Eigentümer: {value}",
    "ui.serviceSheet.stackkit": "StackKit {version}",
    "ui.services.aRegisteredNodeIsRequired":
      "Ein registrierter Node ist erforderlich, bevor Services angebunden werden können.",
    "ui.services.aboutToMove":
      "Du bist dabei, diese Anwendung von {slot} nach {slot} zu verschieben.",
    "ui.services.action.freeze": "Einfrieren",
    "ui.services.action.logs": "Logs",
    "ui.services.action.restart": "Neustarten",
    "ui.services.action.start": "Starten",
    "ui.services.action.stop": "Stoppen",
    "ui.services.action.unfreeze": "Auftauen",
    "ui.services.actionCompletedAndFreshInventory":
      "Aktion abgeschlossen und ein frisches Inventar wurde beobachtet.",
    "ui.services.actionCompletedButNoNewer":
      "Aktion abgeschlossen, aber innerhalb des Aktualitätsfensters des Services ist keine neuere Inventarbeobachtung eingegangen.",
    "ui.services.actionCompletedInventoryIsRefreshing":
      "Aktion abgeschlossen; das Inventar wird aktualisiert.",
    "ui.services.actionTitle": "{action} {name}",
    "ui.services.addACatalogApplicationOr":
      "Füge eine Katalog-Anwendung hinzu oder importiere nicht verwaltetes Node-Inventar.",
    "ui.services.addANewApplication": "Neue Anwendung hinzufügen",
    "ui.services.addService": "Service hinzufügen",
    "ui.services.adoptIntoManagement": "In die Verwaltung übernehmen…",
    "ui.services.all": "Alle",
    "ui.services.anyRuntimeInventoryShownAbove":
      "Das oben angezeigte Runtime-Inventar bleibt schreibgeschützt. Katalog-, Import-, Verifizierungs- und Migrationsaktionen bleiben deaktiviert, bis sich die Service-Registry erholt.",
    "ui.services.application": "Anwendung",
    "ui.services.applicationIsAlreadyMovingOr":
      "Die Anwendung wird bereits verschoben oder wartet auf Verifizierung.",
    "ui.services.applicationManagementIsTemporarilyUnavailable":
      "Anwendungsverwaltung vorübergehend nicht verfügbar",
    "ui.services.applicationManagementIsUnavailableUntil":
      "Die Anwendungsverwaltung ist nicht verfügbar, bis sich die Service-Registry erholt.",
    "ui.services.applicationMove": "Anwendung verschieben",
    "ui.services.applicationNeedsAStableRunning":
      "Die Anwendung benötigt einen stabilen laufenden oder gestoppten Zustand, bevor sie verschoben werden kann.",
    "ui.services.applicationPlacementIsTemporarilyUnavailable":
      "Anwendungsplatzierung vorübergehend nicht verfügbar",
    "ui.services.applications": "Anwendungen",
    "ui.services.applicationsAppearAfterStackkitsRuntime":
      "Anwendungen erscheinen, sobald StackKits-Runtime-Fakten beobachtet wurden.",
    "ui.services.applicationsShown.one":
      "{shown} angezeigt · {count} Anwendung",
    "ui.services.applicationsShown.other":
      "{shown} angezeigt · {count} Anwendungen",
    "ui.services.approveAction": "{action} freigeben",
    "ui.services.capacityTelemetryRefreshFailed":
      "Aktualisierung der Kapazitäts-Telemetrie fehlgeschlagen",
    "ui.services.catalog": "Katalog",
    "ui.services.catalogServiceCouldNotBe":
      "Katalog-Service konnte nicht hinzugefügt werden.",
    "ui.services.clearTheFilterToShow":
      "Setze den Filter zurück, um alle Anwendungen anzuzeigen.",
    "ui.services.cloudVps": "Cloud-VPS",
    "ui.services.componentCount.one": "{count} Komponente",
    "ui.services.componentCount.other": "{count} Komponenten",
    "ui.services.components": "Komponenten",
    "ui.services.componentsOn.one": "{count} Komponente auf {server}",
    "ui.services.componentsOn.other": "{count} Komponenten auf {server}",
    "ui.services.confirmGoverned":
      "Die gesteuerte Aktion „{action}“ für {name} freigeben? Nach der Änderung läuft ein StackKits-Verifizierungsdurchlauf, bevor sich der gemessene Zustand ändert.",
    "ui.services.confirmLock":
      "{name} sperren? Solange die Sperre besteht, werden Starten, Stoppen und Neustarten abgelehnt, auch über ein Stack-Apply. Der Service läuft weiter.",
    "ui.services.confirmUnlock":
      "{name} entsperren? Gesteuerte Änderungen sind dann wieder möglich.",
    "ui.services.connectNodesToYourHomelab":
      "Verbinde Nodes mit deinem Homelab, um die Anwendungsplatzierung zu verwalten.",
    "ui.services.deleteArchivedService": "Archivierten Service löschen?",
    "ui.services.desired": "Soll",
    "ui.services.desiredObserved": "Soll {desired} · Beobachtet {observed}",
    "ui.services.discoveredOn":
      "Auf {name} erkannt — nicht von kombify verwaltet",
    "ui.services.displayName": "Anzeigename",
    "ui.services.dragAndDropStaysDisabled":
      "Drag-and-drop bleibt deaktiviert, bis Deployment, Health-Verifizierung, Umschaltung und Quell-Drain von einem echten Runtime-Executor übernommen werden.",
    "ui.services.dropManagedApplicationsHere":
      "Verwaltete Anwendungen hier ablegen",
    "ui.services.error": "Fehler",
    "ui.services.failedToDeleteArchivedService":
      "Archivierter Service konnte nicht gelöscht werden.",
    "ui.services.failedToInitiateMigration":
      "Migration konnte nicht gestartet werden.",
    "ui.services.failedToVerifyService":
      "Service konnte nicht verifiziert werden.",
    "ui.services.fetchLogs": "Logs abrufen",
    "ui.services.freshnessValue": "Aktualität: {value}",
    "ui.services.health": "Health",
    "ui.services.importAsObserved": "Als beobachtet importieren",
    "ui.services.importUnmanaged": "Nicht Verwaltetes importieren",
    "ui.services.internalAddressOnlyOpenIt":
      "Nur interne Adresse; öffne sie aus dem Netzwerk des Nodes.",
    "ui.services.jobId": "Job {id}",
    "ui.services.loadOlderLogs": "Ältere Logs laden",
    "ui.services.loadingCanonicalServiceInventory":
      "Kanonisches Service-Inventar wird geladen",
    "ui.services.localNode": "Lokaler Node",
    "ui.services.managedProviderTarget": "Verwaltetes Anbieterziel",
    "ui.services.migrationJobIsActive": "Der Migrations-Job ist aktiv.",
    "ui.services.moveApplication": "Anwendung verschieben",
    "ui.services.moveHere": "{name} hierher verschieben",
    "ui.services.moveSteps": "Schritte des Verschiebens:",
    "ui.services.moveToAnotherNode": "Auf anderen Node verschieben…",
    "ui.services.mutationsRequireOwnerApprovalA":
      "Änderungen erfordern die Freigabe des Owners; vor jeder Änderung des gemessenen Zustands läuft ein StackKits-Verifizierungsdurchlauf.",
    "ui.services.newApplication": "Neue Anwendung",
    "ui.services.noApplicationsDeployed": "Keine Anwendungen deployt",
    "ui.services.noApplicationsMatching":
      "Keine Anwendungen mit Status {status}",
    "ui.services.noApplicationsReported": "Keine Anwendungen gemeldet",
    "ui.services.noAvailableTargetNodeFor":
      "Kein verfügbarer Ziel-Node für diese Anwendung.",
    "ui.services.noCanonicalRuntimeObservationYet":
      "Noch keine kanonische Runtime-Beobachtung — Registry-Projektion wird angezeigt.",
    "ui.services.noGovernedActionHasRun":
      "In dieser Sitzung wurde noch keine gesteuerte Aktion ausgeführt.",
    "ui.services.noHostname": "Kein Hostname",
    "ui.services.noNodesRegistered": "Keine Nodes registriert",
    "ui.services.noOperationsAvailable": "Keine Vorgänge verfügbar.",
    "ui.services.noSystemServices": "Keine System-Services",
    "ui.services.node": "Node",
    "ui.services.nodeBoundApplications": "An einen Node gebundene Anwendungen",
    "ui.services.nodeCapabilities": "Node-Fähigkeiten",
    "ui.services.nodeColumn": "Spalte für Node {name}",
    "ui.services.observed": "Beobachtet",
    "ui.services.observedTechnicalComponentsWithoutApplication":
      "Beobachtete technische Komponenten ohne Steuerung auf Anwendungsebene",
    "ui.services.observedUnmanagedApplicationsMustBe":
      "Beobachtete, nicht verwaltete Anwendungen müssen übernommen werden, bevor sie verschoben werden können.",
    "ui.services.open": "Öffnen",
    "ui.services.operationValue": "Vorgang: {value}",
    "ui.services.ownershipUnknown": "Eigentümer unbekannt",
    "ui.services.pending": "Ausstehend",
    "ui.services.placement.cloud": "Cloud",
    "ui.services.placement.local": "Lokal",
    "ui.services.placement.managed": "Verwaltete Workload",
    "ui.services.placement.unknown": "Platzierung unbekannt",
    "ui.services.placementAndMigrationControlsStay":
      "Platzierungs- und Migrationssteuerung bleiben deaktiviert, bis sich die Service-Registry erholt.",
    "ui.services.placementBoard": "Platzierungsübersicht",
    "ui.services.placementUnknown": "Platzierung unbekannt",
    "ui.services.port": "Port",
    "ui.services.ramUsedOf": "{used} GB / {total} GB",
    "ui.services.refreshLogs": "Logs aktualisieren",
    "ui.services.relocatingOnBackend": "Verlagerung im Backend läuft...",
    "ui.services.removeOldCopy": "Alte Kopie entfernen…",
    "ui.services.retryManagementData": "Verwaltungsdaten erneut laden",
    "ui.services.running": "Läuft",
    "ui.services.runtimeInventoryCouldNotBe":
      "Runtime-Inventar konnte nicht geladen werden.",
    "ui.services.runtimeMigrationIsNotEnabled":
      "Runtime-Migration ist noch nicht aktiviert",
    "ui.services.runtimeMigrationUnavailable":
      "Runtime-Migration nicht verfügbar",
    "ui.services.runtimeServiceMigrationIsNot":
      "Die Runtime-Service-Migration ist in diesem Deployment nicht aktiviert.",
    "ui.services.runtimeTarget": "Runtime-Ziel",
    "ui.services.selectANodeAndCatalog":
      "Wähle einen Node und einen Katalog-Service aus.",
    "ui.services.selectANodeThenEnter":
      "Wähle einen Node aus und gib dann einen Service-Namen ein.",
    "ui.services.selected": "Ausgewählt: {name}",
    "ui.services.service": "Service",
    "ui.services.serviceActionDidNotComplete":
      "Die Service-Aktion wurde nicht abgeschlossen.",
    "ui.services.serviceActionDidNotReturn":
      "Die Service-Aktion hat keine Job-ID zurückgegeben.",
    "ui.services.serviceActionFailed": "Die Service-Aktion ist fehlgeschlagen.",
    "ui.services.serviceFallback": "Service",
    "ui.services.serviceLogCollectionDidNot":
      "Die Erfassung der Service-Logs wurde nicht abgeschlossen.",
    "ui.services.serviceLogsCouldNotBe":
      "Service-Logs konnten nicht geladen werden.",
    "ui.services.serviceMode": "Service-Modus",
    "ui.services.serviceRegistryCouldNotBe":
      "Service Registry konnte nicht geladen werden.",
    "ui.services.services": "Services",
    "ui.services.servicesMode": "Services-Modus",
    "ui.services.showingLastVerifiedCapacityTelemetry":
      "Zeigt die zuletzt verifizierte Kapazitäts-Telemetrie",
    "ui.services.stackkitsApplicationModel": "StackKits-Anwendungsmodell",
    "ui.services.startMove": "Verschieben starten",
    "ui.services.startsControlledMove":
      "Dadurch wird ein kontrolliertes Verschieben von {slot} gestartet.",
    "ui.services.statusValue": "Status: {value}",
    "ui.services.storage": "Speicher",
    "ui.services.storageFreeOf": "{free} GB frei / {total} GB",
    "ui.services.systemServices": "System-Services",
    "ui.services.targetIsReadyForVerification":
      "Das Ziel ist bereit zur Verifizierung.",
    "ui.services.targetNode": "Ziel-Node",
    "ui.services.targetValue": "Ziel: {value}",
    "ui.services.telemetryPending": "Telemetrie ausstehend",
    "ui.services.temporaryInstance":
      "Auf {slot} wird eine temporäre Instanz deployt.",
    "ui.services.theApplicationConfigWillBe":
      "Die Anwendungskonfiguration wird auf den Ziel-Node dupliziert.",
    "ui.services.theLastLogPageWas": "Die letzte Log-Seite war leer.",
    "ui.services.theLastSuccessfulSnapshotRemains":
      "Der letzte erfolgreiche Snapshot bleibt sichtbar, bis dieses StackKit-Deployment einen Ersatz oder einen expliziten Ausfallnachweis meldet.",
    "ui.services.thisHomelab": "dieses Homelab",
    "ui.services.thisPermanentlyDeletesTheArchived":
      "Dadurch wird der archivierte Service dauerhaft von seinem Quell-Node gelöscht.",
    "ui.services.type": "Typ:",
    "ui.services.type2": "Typ",
    "ui.services.unclassifiedRuntimeComponentsAppearHere":
      "Nicht klassifizierte Runtime-Komponenten erscheinen hier.",
    "ui.services.unmanagedServiceCouldNotBe":
      "Nicht verwalteter Service konnte nicht importiert werden.",
    "ui.services.uponYourManualVerificationThe":
      "Nach deiner manuellen Verifizierung wird die alte Instanz deaktiviert und kann dauerhaft entfernt werden.",
    "ui.services.url": "URL",
    "ui.services.verifyFinish": "Verifizieren & Abschließen",
    "ui.services.verifying": "· wird verifiziert",
    "ui.services.waitingForRuntimeDeployment":
      "Warte auf Runtime-Deployment...",
    "ui.services.youCanTestTheNew":
      "Du kannst die neue Instanz testen, während die alte aktiv bleibt.",
    "ui.servicesApplicationId.addressClass": "Adressklasse",
    "ui.servicesApplicationId.applicationDetails": "Anwendungsdetails",
    "ui.servicesApplicationId.applicationDetailsCouldNotBe":
      "Anwendungsdetails konnten nicht geladen werden.",
    "ui.servicesApplicationId.applicationKey": "Anwendungsschlüssel",
    "ui.servicesApplicationId.backToApplications": "Zurück zu den Anwendungen",
    "ui.servicesApplicationId.freshness": "Aktualität",
    "ui.servicesApplicationId.impact": "Auswirkung",
    "ui.servicesApplicationId.kitDeployment": "Kit-Deployment",
    "ui.servicesApplicationId.lifecycle": "Lebenszyklus",
    "ui.servicesApplicationId.management": "Verwaltung",
    "ui.servicesApplicationId.noReachableAddressReported":
      "Keine erreichbare Adresse gemeldet",
    "ui.servicesApplicationId.openService": "Dienst öffnen",
    "ui.servicesApplicationId.role": "Rolle",
    "ui.servicesApplicationId.runtimeAssignment": "Runtime-Zuweisung",
    "ui.servicesApplicationId.server": "Server",
    "ui.servicesApplicationId.source": "Quelle",
    "ui.settings.account": "Konto",
    "ui.settings.appearance": "Darstellung",
    "ui.settings.appearanceMode": "Darstellungsmodus",
    "ui.settings.applyExactPlan": "Genauen Plan anwenden",
    "ui.settings.applying": "Wird angewendet...",
    "ui.settings.cleanUpTestResidue": "Testrückstände bereinigen",
    "ui.settings.cleanupFailed": "Bereinigung fehlgeschlagen",
    "ui.settings.cleanupReviewFailed": "Prüfung der Bereinigung fehlgeschlagen",
    "ui.settings.dangerZone": "Gefahrenzone",
    "ui.settings.dashboard": "Dashboard",
    "ui.settings.dashboardLayout": "Dashboard-Layout",
    "ui.settings.decommissionsTheDeploymentSManaged":
      "Nimmt die verwaltete Runtime des Deployments außer Betrieb und entfernt den Eintrag. Um stattdessen einen einzelnen Server zu entfernen, nutze dessen Außerbetriebnahme-Aktion am Server.",
    "ui.settings.delete": "Löschen",
    "ui.settings.deleteADeployment": "Ein Deployment löschen",
    "ui.settings.deleteNamed": "„{name}“ löschen?",
    "ui.settings.deleteThisDeployment": "Dieses Deployment löschen",
    "ui.settings.deleting": "Wird gelöscht...",
    "ui.settings.deletionFailed": "Löschen fehlgeschlagen",
    "ui.settings.failedToLoadDeployments":
      "Deployments konnten nicht geladen werden",
    "ui.settings.failedToLoadHomelabIdentity":
      "Homelab-Identität konnte nicht geladen werden",
    "ui.settings.failedToSaveHomelabIdentity":
      "Homelab-Identität konnte nicht gespeichert werden",
    "ui.settings.flag": "Flag",
    "ui.settings.float": "Float",
    "ui.settings.followAccount": "Konto folgen",
    "ui.settings.followFinish": "Finish folgen",
    "ui.settings.homelabIdentity": "Homelab-Identität",
    "ui.settings.lightOrDarkAndThe":
      "Hell oder dunkel und das Oberflächen-Finish. Das Finish folgt der Standardeinstellung deines kombify-Kontos, bis dieses Gerät ein eigenes wählt.",
    "ui.settings.loadingDeployments": "Deployments werden geladen...",
    "ui.settings.logout": "Abmelden",
    "ui.settings.managedByKombifyCloud": "Verwaltet von kombify Cloud.",
    "ui.settings.mode": "Modus",
    "ui.settings.navigationPreview": "Navigationsvorschau",
    "ui.settings.navigationPreviewShape": "Form der Navigationsvorschau",
    "ui.settings.noDeploymentsToDelete": "Keine Deployments zum Löschen.",
    "ui.settings.noHomelabIdentityConfigured":
      "Keine Homelab-Identität konfiguriert.",
    "ui.settings.noVerifiedTestResidueRemains":
      "Es sind keine verifizierten Testrückstände mehr vorhanden.",
    "ui.settings.noVerifiedTestResidueWas":
      "Es wurden keine verifizierten Testrückstände gefunden.",
    "ui.settings.overlap": "Überlappung",
    "ui.settings.pruneQualifies":
      "Nur Projektionen mit authentifiziertem Eigentümer {owner} und strikter Übereinstimmung {strict} kommen infrage. Aktive Managed Runtimes, Provider-Ressourcen, neue Nodes, Demodaten und normale Homelab-Daten bleiben unverändert.",
    "ui.settings.reviewCleanup": "Bereinigung prüfen",
    "ui.settings.reviewVerifiedTestResidue":
      "Verifizierte Testrückstände prüfen",
    "ui.settings.reviewing": "Wird geprüft...",
    "ui.settings.reviewsExactOwnerScopedE2e":
      "Prüft exakt dem Eigentümer zugeordnete E2E- und fehlgeschlagene Runtime-Projektionen. Provider-Ressourcen, aktive Leases, neue Nodes und normale Homelab-Daten bleiben unverändert.",
    "ui.settings.savedOnDevice": "Auf diesem Gerät für dein Konto gespeichert.",
    "ui.settings.settings": "Einstellungen",
    "ui.settings.signedIn": "Angemeldet",
    "ui.settings.surface": "Oberfläche",
    "ui.settings.surfaceFinish": "Oberflächen-Finish",
    "ui.settings.tether": "Tether",
    "ui.settings.theDemoDeploymentIsProtected":
      "Das Demo-Deployment ist geschützt und kann nicht gelöscht werden.",
    "ui.settings.thisDecommissionsTheManagedRuntime":
      "Dadurch wird die verwaltete Runtime außer Betrieb genommen und der Deployment-Eintrag entfernt. Das lässt sich nicht rückgängig machen.",
    "ui.settings.thisEntryIsNotVerified":
      "Dieser Eintrag ist kein verifizierter Testrückstand und kann nicht durch die Projektionsbereinigung entfernt werden.",
    "ui.settings.thisExactPlanIsBound":
      "Dieser genaue Plan ist an den unten stehenden Digest gebunden. Es wird keine Infrastruktur zerstört.",
    "ui.settings.typeToConfirm": "Zur Bestätigung {slot} eingeben",
    "ui.shortcuts.closeDialogDeselect": "Dialog schließen/Auswahl aufheben",
    "ui.shortcuts.focusSearchInput": "Suchfeld fokussieren",
    "ui.shortcuts.goToHomeDashboard": "Zu Start/Dashboard gehen",
    "ui.shortcuts.goToMonitoring": "Zum Monitoring gehen",
    "ui.shortcuts.goToServices": "Zu Services gehen",
    "ui.shortcuts.goToSettings": "Zu Einstellungen gehen",
    "ui.shortcuts.goToWallet": "Zur Wallet gehen",
    "ui.shortcuts.jumpToFirstItem": "Zum ersten Eintrag springen",
    "ui.shortcuts.jumpToLastItem": "Zum letzten Eintrag springen",
    "ui.shortcuts.moveDownInList": "In der Liste nach unten",
    "ui.shortcuts.moveUpInList": "In der Liste nach oben",
    "ui.shortcuts.pressToClose": "Drücke {slot} zum Schließen",
    "ui.shortcuts.pressToShow":
      "Drücke jederzeit {slot}, um diese Hilfe anzuzeigen",
    "ui.shortcuts.refreshCurrentView": "Aktuelle Ansicht aktualisieren",
    "ui.shortcuts.selectOpenItem": "Eintrag auswählen/öffnen",
    "ui.shortcuts.showKeyboardShortcutsHelp": "Hilfe zu Tastenkürzeln anzeigen",
    "ui.shortcutsHelp.dialogs": "Dialoge",
    "ui.shortcutsHelp.esc": "Esc",
    "ui.shortcutsHelp.listNavigation": "Listennavigation",
    "ui.shortcutsHelp.pageNavigation": "Seitennavigation",
    "ui.sidebarNav.kombifyTechstack": "kombify-Techstack",
    "ui.sshKeygen.ed25519NotSupportedInThis":
      "Ed25519 wird in diesem Browser nicht unterstützt. Versuche stattdessen RSA.",
    "ui.sshKeygen.unsupportedAlgorithm":
      "Nicht unterstützter Algorithmus: {algorithm}",
    "ui.stackImport.exportIntro":
      "Exportiere diese StackKit-Deployment-Konfiguration als {file}. Du kannst die Datei bearbeiten und später wieder importieren.",
    "ui.stackImport.intro":
      "Importiere eine {file}-Datei, um ein StackKit-Deployment in deinem Homelab einzurichten. Ältere {legacy}-Dateien werden weiterhin akzeptiert.",
    "ui.stackImport.orPaste": "oder einfügen",
    "ui.stackImport.validating": "Wird validiert...",
    "ui.stackImportExportModal.allRequiredFieldsArePresent":
      "Alle Pflichtfelder sind vorhanden. Nach dem Import startet der Unifier-Prozess und ermittelt das passende StackKit für deine Konfiguration.",
    "ui.stackImportExportModal.configurationErrors": "Konfigurationsfehler",
    "ui.stackImportExportModal.configurationIsValid":
      "Konfiguration ist gültig",
    "ui.stackImportExportModal.copyDiagnostics": "Diagnose kopieren",
    "ui.stackImportExportModal.copyDiagnosticsForDevelopers":
      "Diagnose kopieren (für Entwickler)",
    "ui.stackImportExportModal.couldNotReadFile":
      "Datei konnte nicht gelesen werden",
    "ui.stackImportExportModal.dragFileHereOr": "Datei hierher ziehen oder",
    "ui.stackImportExportModal.exportStackkitDeploymentSpec":
      "StackKit-Deployment-Spezifikation exportieren",
    "ui.stackImportExportModal.ifTheProblemPersistsCopy":
      "Wenn das Problem weiterhin besteht: Kopiere die Diagnose und teile sie mit den Entwicklern.",
    "ui.stackImportExportModal.importStackkitDeploymentSpec":
      "StackKit-Deployment-Spezifikation importieren",
    "ui.stackImportExportModal.importStartSetup":
      "Importieren & Einrichtung starten",
    "ui.stackImportExportModal.importingConfiguration":
      "Konfiguration wird importiert...",
    "ui.stackImportExportModal.metadataAndIntents": "• Metadaten und Intents",
    "ui.stackImportExportModal.noContentFound": "Kein Inhalt gefunden",
    "ui.stackImportExportModal.noContentToImport":
      "Kein Inhalt zum Importieren",
    "ui.stackImportExportModal.noStackkitDeploymentIsAvailable":
      "Es ist kein StackKit-Deployment zum Exportieren verfügbar",
    "ui.stackImportExportModal.nodeDefinitionsWithoutCredentials":
      "• Node-Definitionen (ohne Zugangsdaten)",
    "ui.stackImportExportModal.pasteYourStackSpecYaml":
      "# Füge hier deine stack-spec.yaml ein...",
    "ui.stackImportExportModal.recommended": "(empfohlen)",
    "ui.stackImportExportModal.selectFile": "Datei auswählen",
    "ui.stackImportExportModal.serviceConfigurations":
      "• Dienstkonfigurationen",
    "ui.stackImportExportModal.sshKeysAndSecretsAre":
      "SSH-Schlüssel und Secrets werden nicht exportiert. Sie müssen nach einem Import neu konfiguriert werden.",
    "ui.stackImportExportModal.stackNameAndConfiguration":
      "• Stack-Name und Konfiguration",
    "ui.stackImportExportModal.supportedYamlYmlJson":
      "Unterstützt: .yaml, .yml, .json",
    "ui.stackImportExportModal.systemNetworkAndSecuritySettings":
      "• System-, Netzwerk- und Sicherheitseinstellungen",
    "ui.stackImportExportModal.tipIfYouSeeAn":
      "Tipp: Wenn du statt JSON eine HTML-Seite siehst, ist möglicherweise der Backend-Proxy oder deine Sitzung/Authentifizierung defekt. Bitte versuche es erneut.",
    "ui.stackImportExportModal.validateConfiguration":
      "Konfiguration validieren",
    "ui.stackImportExportModal.validationFailed": "Validierung fehlgeschlagen",
    "ui.stackImportExportModal.whatWillBeExported": "Was wird exportiert?",
    "ui.stackImportExportModal.youWillBeRedirectedTo":
      "Du wirst in Kürze zur Einrichtungsseite weitergeleitet.",
    "ui.stacksCreating.creatingStackkitDeploymentKombifyTechstack":
      "StackKit-Deployment wird erstellt | kombify-Techstack",
    "ui.stacksCreating.openOperations": "Vorgänge öffnen",
    "ui.stacksCreating.reviewAndStartStackkitRollout":
      "StackKit-Rollout prüfen und starten",
    "ui.stacksCreatingCreation-controller.additionalNode": "Zusätzlicher Node",
    "ui.stacksCreatingCreation-controller.afterFailedAttemptsTheBackend":
      "Nach {MAX_POLL_ERRORS} fehlgeschlagenen Versuchen war das Backend nicht erreichbar. Die Seite versucht es mit Backoff weiter; du kannst auch deine Netzwerkverbindung prüfen und neu laden, um fortzufahren.",
    "ui.stacksCreatingCreation-controller.configurationPrepared":
      "Konfiguration vorbereitet",
    "ui.stacksCreatingCreation-controller.connectYourNodeToContinue":
      "Verbinde deinen Node, um den Rollout fortzusetzen",
    "ui.stacksCreatingCreation-controller.connectionToServerLost":
      "Verbindung zum Server verloren",
    "ui.stacksCreatingCreation-controller.continueStackKitDeploymentOnConnected":
      "StackKit-Deployment auf verbundenem Node fortsetzen",
    "ui.stacksCreatingCreation-controller.continueStackKitOnConnectedNode":
      "StackKit auf verbundenem Node fortsetzen",
    "ui.stacksCreatingCreation-controller.continueStackKitRolloutOnConnected":
      "StackKit-Rollout auf verbundenem Node fortsetzen",
    "ui.stacksCreatingCreation-controller.couldNotCheckRemoteSSH":
      "Der Fortschritt der Remote-SSH-Registrierung konnte nicht geprüft werden.",
    "ui.stacksCreatingCreation-controller.couldNotPrepareTheConnection":
      "Der Verbindungsbefehl konnte nicht vorbereitet werden.",
    "ui.stacksCreatingCreation-controller.couldNotResumeTheEarlier":
      "Die frühere Registrierung konnte nicht fortgesetzt werden. Starte stattdessen einen neuen Versuch.",
    "ui.stacksCreatingCreation-controller.couldNotResumeTheOverdue":
      "Das überfällige Warten auf die verwaltete Runtime konnte nicht fortgesetzt werden.",
    "ui.stacksCreatingCreation-controller.couldNotRetryRemoteSSH":
      "Die Remote-SSH-Registrierung konnte nicht wiederholt werden.",
    "ui.stacksCreatingCreation-controller.couldNotStartLifecycleRetry":
      "Der Lifecycle-Wiederholungsversuch konnte nicht gestartet werden.",
    "ui.stacksCreatingCreation-controller.couldNotStartRolloutRetry":
      "Der Rollout-Wiederholungsversuch konnte nicht gestartet werden.",
    "ui.stacksCreatingCreation-controller.creatingStackKitDeployment":
      "StackKit-Deployment wird erstellt",
    "ui.stacksCreatingCreation-controller.guardHasNotReportedA":
      "Der Guard hat für diesen Node noch keinen frischen Heartbeat gemeldet. Versuche die SSH-Registrierung über die gespeicherte Verbindung erneut oder prüfe, ob der Agent auf dem Server läuft.",
    "ui.stacksCreatingCreation-controller.jobIsNotBeingProcessed":
      "Der Job wird nicht verarbeitet",
    "ui.stacksCreatingCreation-controller.jobWasCanceled":
      "Der Job wurde abgebrochen",
    "ui.stacksCreatingCreation-controller.kombifyRequestedTheAdditionalManaged":
      "kombify hat den zusätzlichen verwalteten Node angefordert. Du kannst vom Dashboard aus weitermachen, während die Registrierung abschließt.",
    "ui.stacksCreatingCreation-controller.managedNodeRequestIsStill":
      "Die Anfrage für den verwalteten Node läuft noch",
    "ui.stacksCreatingCreation-controller.managedRolloutRecoveryWasNot":
      "Die Wiederherstellung des verwalteten Rollouts wurde nicht gestartet, weil der wartende Job keine exakte Lease-Referenz hat.",
    "ui.stacksCreatingCreation-controller.missingJobReferencePleaseStart":
      "Job-Referenz fehlt. Bitte starte den Einrichtungsassistenten erneut.",
    "ui.stacksCreatingCreation-controller.newStackKitDeployment":
      "Neues StackKit-Deployment",
    "ui.stacksCreatingCreation-controller.noJobFound": "Kein Job gefunden",
    "ui.stacksCreatingCreation-controller.nodeConnected": "Node verbunden",
    "ui.stacksCreatingCreation-controller.nodeConnectionReady":
      "Node-Verbindung bereit",
    "ui.stacksCreatingCreation-controller.nodeRegistrationDidNotReturn":
      "Die Node-Registrierung hat keinen Erstellungs-Job zurückgegeben.",
    "ui.stacksCreatingCreation-controller.nodeRegistrationReady":
      "Node-Registrierung bereit",
    "ui.stacksCreatingCreation-controller.recoveringRolloutOnTheExact":
      "Rollout auf der exakten bestehenden verwalteten VM wird wiederhergestellt...",
    "ui.stacksCreatingCreation-controller.remoteSSHEnrollmentFailed":
      "Remote-SSH-Registrierung fehlgeschlagen.",
    "ui.stacksCreatingCreation-controller.reportedAFreshGuardHeartbeat":
      "{name} hat einen frischen Guard-Heartbeat gemeldet und ist in der Node-Projektion sichtbar.",
    "ui.stacksCreatingCreation-controller.restoreDrill":
      "Wiederherstellungsübung",
    "ui.stacksCreatingCreation-controller.resumingTheEarlierNodeRegistration":
      "Früherer Node-Registrierungsversuch wird fortgesetzt…",
    "ui.stacksCreatingCreation-controller.retryDeployment":
      "Deployment erneut versuchen",
    "ui.stacksCreatingCreation-controller.retryRollout":
      "Rollout erneut versuchen",
    "ui.stacksCreatingCreation-controller.retryServerRequest":
      "Serveranfrage erneut versuchen",
    "ui.stacksCreatingCreation-controller.retrying": "Neuer Versuch...",
    "ui.stacksCreatingCreation-controller.retryingDeployment":
      "Deployment wird erneut versucht...",
    "ui.stacksCreatingCreation-controller.retryingRolloutOnTheExisting":
      "Rollout auf der bestehenden verwalteten VM wird erneut versucht...",
    "ui.stacksCreatingCreation-controller.retryingSSHEnrollmentOnThe":
      "SSH-Registrierung über die gespeicherte Verbindung wird erneut versucht…",
    "ui.stacksCreatingCreation-controller.retryingServerProvisioning":
      "Serverbereitstellung wird erneut versucht...",
    "ui.stacksCreatingCreation-controller.rolloutRecoveryDidNotReturn":
      "Die Rollout-Wiederherstellung hat keine job_id zurückgegeben.",
    "ui.stacksCreatingCreation-controller.rolloutRetryRequiresTheExact":
      "Für den erneuten Rollout-Versuch werden der exakte fehlgeschlagene Job und der Lease der verwalteten VM benötigt.",
    "ui.stacksCreatingCreation-controller.serviceVerification":
      "Dienstverifizierung",
    "ui.stacksCreatingCreation-controller.sessionExpired": "Sitzung abgelaufen",
    "ui.stacksCreatingCreation-controller.simulationGate":
      "Simulationsschranke",
    "ui.stacksCreatingCreation-controller.stackkitRollout": "StackKit-Rollout",
    "ui.stacksCreatingCreation-controller.stackkitRuntimeVerificationMissing":
      "StackKit-Runtime-Verifizierung fehlt",
    "ui.stacksCreatingCreation-controller.startFreshAttempt":
      "Neuen Versuch starten",
    "ui.stacksCreatingCreation-controller.theAddNodeRequestHas":
      "Die Anfrage „Node hinzufügen“ läuft seit über 2 Minuten. Dieser Pfad soll den zusätzlichen Node nur anfordern oder vorbereiten, nicht den vollständigen StackKit-Rollout ausführen. Öffne die Vorgänge, um zu prüfen, ob die Node-Anfrage erstellt wurde, und versuche „Node hinzufügen“ erneut, wenn kein neuer Node erscheint.",
    "ui.stacksCreatingCreation-controller.theAdditionalNodeRegistrationIs":
      "Die Registrierung des zusätzlichen Nodes ist für das bestehende Homelab bereit.",
    "ui.stacksCreatingCreation-controller.theBackendCanceledThisJob":
      "Das Backend hat diesen Job abgebrochen, bevor er abgeschlossen war. Versuche es erneut, um ab dem letzten sicheren Checkpoint fortzufahren.",
    "ui.stacksCreatingCreation-controller.theCompletedRuntimeJobDid":
      "Der abgeschlossene Runtime-Job hat keinen verifizierten Runtime-Nachweis zurückgegeben. Versuche den Rollout erneut, um die Installation zu verifizieren.",
    "ui.stacksCreatingCreation-controller.theNodeProjectionCouldNot":
      "Die Node-Projektion konnte nicht geprüft werden. Der Kopplungsbefehl bleibt verfügbar, aber diese Seite meldet ohne frischen Guard-Heartbeat keine Verbindung.",
    "ui.stacksCreatingCreation-controller.thePreviouslyVerifiedGuardHeartbeat":
      "Der zuvor verifizierte Guard-Heartbeat ist nicht mehr frisch. Es wird auf aktuelle Verbindungsnachweise gewartet.",
    "ui.stacksCreatingCreation-controller.theProvisioningJobHasBeen":
      "Der Bereitstellungs-Job hängt seit über 2 Minuten ohne Fortschritt. Das bedeutet meist, dass der Orchestrator nicht läuft oder abgestürzt ist.\n\nPrüfe die Server-Logs: docker compose logs techstack\nVersuche einen Neustart: docker compose restart techstack",
    "ui.stacksCreatingCreation-controller.theReservedNodeCouldNot":
      "Der reservierte Node konnte nicht geprüft werden. Versuche die SSH-Registrierung über die gespeicherte Verbindung erneut, sobald das Server-Inventar wieder verfügbar ist.",
    "ui.stacksCreatingCreation-controller.thisPageRequiresAJob":
      "Diese Seite benötigt eine job_id vom Einrichtungsassistenten. Bitte starte die Einrichtung über /stacks/new neu.",
    "ui.stacksCreatingCreation-controller.validatingTheSelectedStackKitAnd":
      "Das ausgewählte StackKit wird validiert und der Node vorbereitet...",
    "ui.stacksCreatingCreation-controller.yourRemoteNodeConfigurationIs":
      "Deine Remote-Node-Konfiguration ist bereit für den Rollout",
    "ui.stacksCreatingCreation-controller.yourSessionIsNoLonger":
      "Deine Sitzung ist nicht mehr gültig, daher kann der Erstellungsfortschritt nicht geprüft werden. Melde dich erneut an und öffne diese Erstellung noch einmal, um sie weiter zu verfolgen.",
    "ui.stacksCreatingCreation-results.backendError":
      "{cleanDetails}\n\nBackend-Fehler:\n{cleanError}",
    "ui.stacksCreatingCreation-results.incidentContext": "Vorfallkontext:",
    "ui.stacksCreatingCreation-results.jobID": "Job-ID: {id}",
    "ui.stacksCreatingCreation-results.runtimeDiagnostics": "Runtime-Diagnose:",
    "ui.stacksCreatingCreation-results.stackID": "Stack-ID: {stackId}",
    "ui.stacksCreatingCreation-results.targetBootstrap": "Ziel-Bootstrap:",
    "ui.stacksCreatingCreationCompletion.guardHeartbeatVerified":
      "Guard-Heartbeat verifiziert",
    "ui.stacksCreatingCreationCompletion.hashPresent": "Hash vorhanden",
    "ui.stacksCreatingCreationCompletion.lastGuardHeartbeat":
      "Letzter Guard-Heartbeat:",
    "ui.stacksCreatingCreationCompletion.managedProvisioningComplete":
      "Verwaltete Bereitstellung abgeschlossen",
    "ui.stacksCreatingCreationCompletion.materialLinked": "Material verknüpft",
    "ui.stacksCreatingCreationCompletion.openHomelab": "Homelab öffnen",
    "ui.stacksCreatingCreationCompletion.owner": "Eigentümer",
    "ui.stacksCreatingCreationCompletion.ownerPrepared":
      "Eigentümer vorbereitet",
    "ui.stacksCreatingCreationCompletion.ownerReady": "Eigentümer bereit",
    "ui.stacksCreatingCreationCompletion.ownerSeedReady":
      "Eigentümer-Seed bereit",
    "ui.stacksCreatingCreationCompletion.recovery": "Wiederherstellung",
    "ui.stacksCreatingCreationCompletion.rolloutComplete":
      "Rollout abgeschlossen",
    "ui.stacksCreatingCreationCompletion.selfHostedRollout":
      "Selbst gehosteter Rollout",
    "ui.stacksCreatingCreationCompletion.techstackNowSeesThisNode":
      "Techstack sieht diesen Node jetzt in der echten Node-Projektion als gesund und rollout-bereit. Die Vorbereitung der Kopplung allein hat diesen Zustand nicht ausgelöst.",
    "ui.stacksCreatingCreationCompletion.theFirstLoginGatewayAnd":
      "Das erste Login-Gateway und die Wiederherstellungsreferenz kommen mit dem StackKit-Rollout („Review + Start“).",
    "ui.stacksCreatingCreationCompletion.theOwnerIdentityDerivesFrom":
      "Die Eigentümer-Identität leitet sich von deinem verknüpften kombify-Cloud-Profil ab.",
    "ui.stacksCreatingCreationCompletion.theOwnerSeedIsPrepared":
      "Der Eigentümer-Seed ist vorbereitet.",
    "ui.stacksCreatingCreationCompletion.yourNode": "Dein Node",
    "ui.stacksCreatingCreationFailure.anErrorOccurred":
      "Ein Fehler ist aufgetreten",
    "ui.stacksCreatingCreationFailure.checkYourNetworkConnection":
      "Prüfe deine Netzwerkverbindung",
    "ui.stacksCreatingCreationFailure.copyErrorDetails":
      "Fehlerdetails kopieren",
    "ui.stacksCreatingCreationFailure.earlierRegistrationAttemptAlreadyCompleted":
      "Früherer Registrierungsversuch bereits abgeschlossen",
    "ui.stacksCreatingCreationFailure.errorDetails": "Fehlerdetails",
    "ui.stacksCreatingCreationFailure.existingManagedVmLeaseReferenced":
      "Bestehender verwalteter VM-Lease referenziert",
    "ui.stacksCreatingCreationFailure.makeSureTheBackendServer":
      "Stelle sicher, dass der Backend-Server läuft",
    "ui.stacksCreatingCreationFailure.nodeConnectionSucceeded":
      "Node-Verbindung erfolgreich",
    "ui.stacksCreatingCreationFailure.operations": "Vorgänge",
    "ui.stacksCreatingCreationFailure.possibleSolutions": "Mögliche Lösungen:",
    "ui.stacksCreatingCreationFailure.resumePreviousAttempt":
      "Vorherigen Versuch fortsetzen",
    "ui.stacksCreatingCreationFailure.reviewYourConfigurationSettings":
      "Prüfe deine Konfigurationseinstellungen",
    "ui.stacksCreatingCreationFailure.sshEnrollmentCompletedAndThe":
      "Die SSH-Registrierung wurde abgeschlossen und der Node ist in deinem Homelab sichtbar. Nur die StackKit-Vorbereitung oder der Rollout ist fehlgeschlagen. Ein erneuter Versuch läuft auf dem verbundenen Node weiter, ohne die SSH-Registrierung zu wiederholen.",
    "ui.stacksCreatingCreationFailure.theExactRetryEndpointValidates":
      "Der genaue Retry-Endpunkt validiert diesen fehlgeschlagenen Job und Lease, bevor er auf dem bestehenden Server fortfährt.",
    "ui.stacksCreatingCreationFailure.theRolloutReachedTheExisting":
      "Der Rollout hat die bestehende verwaltete VM erreicht.",
    "ui.stacksCreatingCreationFailure.thisBrowserReusedAnAttempt":
      "Dieser Browser hat einen Versuchsschlüssel aus einer anderen Übermittlung wiederverwendet. Der frühere Lauf hat möglicherweise bereits eine Node-Registrierung erstellt. Setze diesen Versuch fort oder starte mit einem neuen Schlüssel neu.",
    "ui.stacksCreatingCreationFailure.troubleshooting": "Fehlerbehebung",
    "ui.stacksCreatingCreationFailure.vpsProvisioningCompletedOnlyStackkit":
      "Die VPS-Bereitstellung wurde abgeschlossen. Nur das StackKit-Artefakt, das Domain-Routing oder spätere Rollout-Arbeiten sind fehlgeschlagen.",
    "ui.stacksCreatingCreationInstallCommand.alternativeInstallationMethods":
      "Alternative Installationsmethoden",
    "ui.stacksCreatingCreationInstallCommand.copy": "Kopieren",
    "ui.stacksCreatingCreationInstallCommand.copyInstallationCommandToClipboard":
      "Installationsbefehl in die Zwischenablage kopieren",
    "ui.stacksCreatingCreationInstallCommand.installWorker":
      "Worker installieren",
    "ui.stacksCreatingCreationInstallCommand.kombifyTechstackServerUrlReachable":
      "kombify-Techstack-Server-URL (von Workern erreichbar):",
    "ui.stacksCreatingCreationInstallCommand.openOneHourSimulateDemo":
      "Einstündige Simulate-Demovorschau öffnen",
    "ui.stacksCreatingCreationInstallCommand.previewLifetimeIsLimitedTo":
      "Die Vorschau ist auf eine Stunde begrenzt.",
    "ui.stacksCreatingCreationInstallCommand.runThisCommandOnAll":
      "Führe diesen Befehl auf allen Servern aus, die du in dein Homelab einbinden möchtest:",
    "ui.stacksCreatingCreationInstallCommand.runThisCommandOnThe":
      "Führe diesen Befehl auf dem Server oder Gerät aus, das das echte Ziel für dieses Homelab werden soll. Simulate kann außerdem eine einstündige Demovorschau des geplanten StackKits bereitstellen.",
    "ui.stacksCreatingCreationLease.access": "Zugriff",
    "ui.stacksCreatingCreationLease.billing": "Abrechnung",
    "ui.stacksCreatingCreationLease.desiredState": "Soll-Zustand",
    "ui.stacksCreatingCreationLease.firstLoginAndRecovery":
      "Erste Anmeldung und Wiederherstellung",
    "ui.stacksCreatingCreationLease.host": "Host",
    "ui.stacksCreatingCreationLease.kombifyWillUseTheCaptured":
      "kombify verwendet die erfassten SSH-Verbindungsdaten für den bestehenden Server",
    "ui.stacksCreatingCreationLease.leaseId": "Lease-ID",
    "ui.stacksCreatingCreationLease.live": "Live",
    "ui.stacksCreatingCreationLease.loginGateway": "Login-Gateway",
    "ui.stacksCreatingCreationLease.managedNodeRequested":
      "Verwalteter Node angefordert",
    "ui.stacksCreatingCreationLease.managedRuntimeReady":
      "Verwaltete Runtime bereit",
    "ui.stacksCreatingCreationLease.offering": "Angebot",
    "ui.stacksCreatingCreationLease.openFirstLogin": "Erste Anmeldung öffnen",
    "ui.stacksCreatingCreationLease.provider": "Anbieter",
    "ui.stacksCreatingCreationLease.ready": "Bereit",
    "ui.stacksCreatingCreationLease.remoteServerConnection":
      "Verbindung zum Remote-Server",
    "ui.stacksCreatingCreationLease.requested": "Angefordert",
    "ui.stacksCreatingCreationLease.runtimeProof": "Runtime-Nachweis",
    "ui.stacksCreatingCreationLease.simulateDemoPreview":
      "Simulate-Demovorschau",
    "ui.stacksCreatingCreationLease.stackkitDeployedOnTheLeased":
      "StackKit auf der geleasten Abo-VM bereitgestellt. Die Werte unten stammen aus dem Runtime-Job – keine Demodaten.",
    "ui.stacksCreatingCreationLease.stackkitIdentityHandoffIsMissing":
      "Die StackKit-Identitätsübergabe fehlt",
    "ui.stacksCreatingCreationLease.stackkitReturnedTheOwnerLogin":
      "StackKit hat die Eigentümer-Anmeldung, das Login-Gateway und die Wiederherstellungsreferenzen für diesen verifizierten Cloud-Kit-Rollout zurückgegeben.",
    "ui.stacksCreatingCreationLease.status": "Status:",
    "ui.stacksCreatingCreationLease.theAdditionalSubscriptionVmRequest":
      "Die Anfrage für die zusätzliche Abo-VM ist erfasst. Sie erscheint unter Vorgänge, sobald die Registrierung zurückmeldet.",
    "ui.stacksCreatingCreationLease.theRolloutCompletedButThe":
      "Der Rollout ist abgeschlossen, aber das StackKit hat Eigentümer-Anmeldung, Login-Gateway und Wiederherstellungsausgaben nicht zurückgegeben. Behandle dies als Release-Blocker, bis die Antwort der Runtime-Aktion Folgendes enthält:",
    "ui.stacksCreatingCreationLease.theseStatusesComeFromRuntime":
      "Diese Status stammen aus Antworten der Runtime-Aktionen und dem abschließenden E2E-Nachweis.",
    "ui.stacksCreatingCreationLease.wallet": "Wallet",
    "ui.stacksCreatingCreationLease.yourInstallCommandIsReady":
      "Dein Installationsbefehl ist bereit. kombify-simulate dient, wenn verfügbar, als temporäre Demovorschau und ist auf eine Stunde begrenzt.",
    "ui.stacksCreatingCreationProgress.backToSetupWizard":
      "Zurück zum Einrichtungsassistenten →",
    "ui.stacksCreatingCreationProgress.creationFailed":
      "Erstellung fehlgeschlagen",
    "ui.stacksCreatingCreationProgress.nodeProvisioningIsStillIn":
      "Die Node-Bereitstellung läuft noch",
    "ui.stacksCreatingCreationProgress.pleaseWaitWhileWeRe":
      "Bitte warte, während wir",
    "ui.stacksCreatingCreationProgress.progress": "Fortschritt",
    "ui.stacksCreatingCreationProgress.rolloutIncomplete":
      "Rollout unvollständig",
    "ui.stacksCreatingCreationProgress.runThePairingCommand":
      "Kopplungsbefehl ausführen",
    "ui.stacksCreatingCreationProgress.setupCannotContinue":
      "Die Einrichtung kann nicht fortgesetzt werden",
    "ui.stacksCreatingCreationProgress.theNextCheckIsScheduled":
      "Die nächste Prüfung ist geplant. Dashboard, Dienste und Node-Zugriff bleiben gesperrt, bis die Registrierung bestätigt ist; eine überfällige Prüfung kann hier sicher fortgesetzt werden.",
    "ui.stacksCreatingCreationProgress.theNextManagedRuntimeCheck":
      "Die nächste Prüfung der verwalteten Runtime ist für diesen Vorgang geplant. Bleibt sie nach einer Replikatübergabe oder einem Neustart überfällig, bietet Techstack für genau diesen Node eine Fortsetzen-Aktion an.",
    "ui.stacksCreatingCreationProgress.theRegistrationRequestIsReady":
      "Die Registrierungsanfrage ist bereit, aber der Node ist erst verbunden, wenn in der Node-Projektion ein frischer Guard-Heartbeat erscheint.",
    "ui.stacksCreatingCreationProgress.thisCanTakeAFew":
      "Das kann einige Minuten dauern. Bitte schließe diese Seite nicht.",
    "ui.stacksCreatingCreationProgress.thisMayTakeAFew":
      "Das kann einige Minuten dauern. Du kannst den Fortschritt unten verfolgen.",
    "ui.stacksCreatingCreationProgress.thisPageChecksTheReal":
      "Diese Seite prüft die echte Node-Projektion alle paar Sekunden.",
    "ui.stacksCreatingCreationRequirements.backendRequirementsAreUnavailable":
      "Backend-Anforderungen sind nicht verfügbar",
    "ui.stacksCreatingCreationRequirements.guide": "Anleitung →",
    "ui.stacksCreatingCreationRequirements.optional": "Optional",
    "ui.stacksCreatingCreationRequirements.required": "Erforderlich",
    "ui.stacksCreatingCreationRequirements.requiredCredentials":
      "Erforderliche Zugangsdaten",
    "ui.stacksCreatingCreationRequirements.requirementsYourServersMustMeet":
      "Anforderungen, die deine Server erfüllen müssen",
    "ui.stacksCreatingCreationRequirements.theOrchestratorBlocksRolloutWhen":
      "Der Orchestrator blockiert den Rollout, wenn ein `Required`-Eintrag auf dem Zielserver fehlt. Optionale Einträge erzeugen nur eine Log-Warnung.",
    "ui.stacksCreatingCreationRequirements.thePageIsNotFilling":
      "Die Seite füllt dies nicht mit Schätzungen aus dem Frontend. Führe die Vorbereitung erneut aus, wenn Anforderungen zur Prüfung nötig sind.",
    "ui.stacksCreatingCreationRunStatus.aManualResumeIsEnabled":
      "Ein manuelles Fortsetzen wird erst nach serverseitiger Validierung freigegeben.",
    "ui.stacksCreatingCreationRunStatus.afterCreation": "Nach der Erstellung",
    "ui.stacksCreatingCreationRunStatus.afterYouRunTheOne":
      "Nachdem du den Einzeiler ausgeführt hast, registriert sich der ausgehende Guard bei diesem Homelab. Das Dashboard markiert den Node erst als verbunden, wenn in der Node-Projektion ein frischer Heartbeat vorliegt.",
    "ui.stacksCreatingCreationRunStatus.awaitingHeartbeat":
      "Warte auf Heartbeat",
    "ui.stacksCreatingCreationRunStatus.checkSshHostCredentialsAnd":
      "Prüfe SSH-Host, Zugangsdaten und ob der Server Techstack erreichen kann.",
    "ui.stacksCreatingCreationRunStatus.completed": "Abgeschlossen",
    "ui.stacksCreatingCreationRunStatus.connectMyNode": "Meinen Node verbinden",
    "ui.stacksCreatingCreationRunStatus.connectingToYourNodeOver":
      "Verbindung zu deinem Node über SSH wird hergestellt…",
    "ui.stacksCreatingCreationRunStatus.connectingViaSsh":
      "Verbindung über SSH wird hergestellt…",
    "ui.stacksCreatingCreationRunStatus.continueRolloutOnTheExisting":
      "Rollout auf der bestehenden VM fortsetzen",
    "ui.stacksCreatingCreationRunStatus.copyFailedSelectTheCommand":
      "Kopieren fehlgeschlagen – markiere den Befehl oben",
    "ui.stacksCreatingCreationRunStatus.copyPairingCommand":
      "Kopplungsbefehl kopieren",
    "ui.stacksCreatingCreationRunStatus.currentStatus": "Aktueller Status",
    "ui.stacksCreatingCreationRunStatus.enrolling": "Wird registriert",
    "ui.stacksCreatingCreationRunStatus.enrollmentOverSshFinishedThis":
      "Die Registrierung über SSH ist abgeschlossen. Diese Seite zeigt „Node verbunden“, sobald der Guard einen frischen Heartbeat meldet.",
    "ui.stacksCreatingCreationRunStatus.failed": "Fehlgeschlagen",
    "ui.stacksCreatingCreationRunStatus.generateAFreshCommandTo":
      "Erzeuge einen neuen Befehl, um die Verbindung dieses Nodes fortzusetzen.",
    "ui.stacksCreatingCreationRunStatus.generateAShortLivedCommand":
      "Erzeuge einen kurzlebigen Befehl für diesen Node. Verbindungsdaten werden nicht in Job-Berichten aufbewahrt.",
    "ui.stacksCreatingCreationRunStatus.generateNewCommand":
      "Neuen Befehl erzeugen",
    "ui.stacksCreatingCreationRunStatus.generatePairingCommand":
      "Kopplungsbefehl erzeugen",
    "ui.stacksCreatingCreationRunStatus.kombifyIsInstallingAndEnrolling":
      "kombify installiert und registriert den Guard über SSH auf deinem Server. Du musst keinen Befehl manuell ausführen.",
    "ui.stacksCreatingCreationRunStatus.kombifyWillProvisionTheSubscription":
      "kombify stellt den Abo-Server bereit und fährt dann mit dem StackKit-Rollout fort.",
    "ui.stacksCreatingCreationRunStatus.kombifyWillUseTheRemote":
      "kombify verwendet die im Assistenten erfasste Remote-SSH-Konfiguration.",
    "ui.stacksCreatingCreationRunStatus.nextScheduledCheck":
      "Nächste geplante Prüfung:",
    "ui.stacksCreatingCreationRunStatus.nodeAccess": "Node-Zugriff",
    "ui.stacksCreatingCreationRunStatus.pairingCommandCopied":
      "Kopplungsbefehl kopiert",
    "ui.stacksCreatingCreationRunStatus.pairingCommandReady":
      "Kopplungsbefehl bereit",
    "ui.stacksCreatingCreationRunStatus.pairingCommandUnavailable":
      "Kopplungsbefehl nicht verfügbar",
    "ui.stacksCreatingCreationRunStatus.plannedRemoteTarget":
      "Geplantes Remote-Ziel",
    "ui.stacksCreatingCreationRunStatus.preparingCommand":
      "Befehl wird vorbereitet…",
    "ui.stacksCreatingCreationRunStatus.provisioningInProgress":
      "Bereitstellung läuft",
    "ui.stacksCreatingCreationRunStatus.remoteSshConnectionFailed":
      "Remote-SSH-Verbindung fehlgeschlagen",
    "ui.stacksCreatingCreationRunStatus.remoteTarget": "Remote-Ziel",
    "ui.stacksCreatingCreationRunStatus.resumeEnrollmentOnTheExisting":
      "Registrierung auf der bestehenden VM fortsetzen",
    "ui.stacksCreatingCreationRunStatus.retryConnectionOnSavedServer":
      "Verbindung zum gespeicherten Server erneut versuchen",
    "ui.stacksCreatingCreationRunStatus.retryingSshConnection":
      "SSH-Verbindung wird erneut versucht…",
    "ui.stacksCreatingCreationRunStatus.runThisOneLinerOn":
      "Führe diesen Einzeiler auf dem zusätzlichen Node aus. Der abgeschlossene Registrierungsjob hat nur das Kopplungstoken erstellt; Techstack zeigt „Node verbunden“ erst an, wenn der ausgehende Guard einen frischen Heartbeat meldet und die Node-Projektion gesund ist.",
    "ui.stacksCreatingCreationRunStatus.techstackUrlReachableFromThe":
      "Techstack-URL, die vom Server aus erreichbar ist",
    "ui.stacksCreatingCreationRunStatus.theAdditionalManagedNodeIs":
      "Der zusätzliche verwaltete Node wird diesem Homelab hinzugefügt und erscheint im Node-Dashboard, sobald die Registrierung zurückmeldet.",
    "ui.stacksCreatingCreationRunStatus.theAdditionalNodeIsRegistered":
      "Der zusätzliche Node ist bei diesem Homelab registriert und kann danach Dienste aus diesem StackKit-Deployment erhalten.",
    "ui.stacksCreatingCreationRunStatus.theManagedRuntimeIsNot":
      "Die Managed Runtime ist für den Rollout noch nicht vollständig erreichbar. Das ausstehende Signal kann ihre Adresse, Zugangsdaten oder Registrierung betreffen. Techstack hat die nächste Prüfung geplant.",
    "ui.stacksCreatingCreationRunStatus.theProviderOperationHasNot":
      "Der Provider-Vorgang hat die bestehende Managed Runtime noch nicht an den StackKit-Rollout übergeben. Techstack hat die nächste Prüfung des exakten Leases geplant.",
    "ui.stacksCreatingCreationRunStatus.theScheduledCheckIsOverdue":
      "Die geplante Prüfung ist überfällig. Techstack validiert Stack, Quell-Job und exakten Lease gemeinsam und setzt dann nur den Rollout auf der bestehenden VM fort.",
    "ui.stacksCreatingCreationRunStatus.theSshDetailsArePlanning":
      "Die SSH-Angaben sind Planungsmetadaten. Die aktuelle Guard-Verbindung ist ausgehend über HTTPS und startet mit dem Befehl unten.",
    "ui.stacksCreatingCreationRunStatus.theseAccessPathsAreEnabled":
      "Diese Zugriffspfade werden erst nach einem echten Registrierungssignal aktiviert.",
    "ui.stacksCreatingCreationRunStatus.thisDoesNotCreateAnother":
      "Dadurch wird keine weitere VM erstellt.",
    "ui.stacksCreatingCreationRunStatus.thisPairingTokenHasExpired":
      "Dieses Kopplungstoken ist abgelaufen.",
    "ui.stacksCreatingCreationRunStatus.validatingTheExistingVm":
      "Bestehende VM wird validiert...",
    "ui.stacksCreatingCreationRunStatus.waitingForGuardHeartbeat":
      "Warte auf Guard-Heartbeat…",
    "ui.stacksCreatingCreationRunStatus.waitingForRealConnection":
      "Warte auf echte Verbindung",
    "ui.stacksCreatingCreationRunStatus.youWillReceiveAWorker":
      "Du erhältst einen Worker-Installationsbefehl, um deine Nodes mit diesem StackKit-Deployment zu verbinden.",
    "ui.stacksId.actionPending": "{action} ausstehend",
    "ui.stacksId.actions": "Aktionen",
    "ui.stacksId.add": "+ Hinzufügen",
    "ui.stacksId.addCredential": "Zugangsdaten hinzufügen",
    "ui.stacksId.addCredentialsToEnableAuto":
      "Füge Zugangsdaten hinzu, um Auto-Login und sichere Speicherung zu aktivieren.",
    "ui.stacksId.backToHomelab": "← Zurück zum Homelab",
    "ui.stacksId.configured": "Konfiguriert",
    "ui.stacksId.credentialsConfigured.one":
      "{count} Zugangsdatensatz konfiguriert",
    "ui.stacksId.credentialsConfigured.other":
      "{count} Zugangsdatensätze konfiguriert",
    "ui.stacksId.decommission": "Außer Betrieb nehmen",
    "ui.stacksId.decommissionManagedRuntime":
      "Verwaltete Runtime außer Betrieb nehmen?",
    "ui.stacksId.deleteCredential": "Zugangsdaten löschen?",
    "ui.stacksId.deploymentFallback": "StackKit-Deployment",
    "ui.stacksId.edit": "Bearbeiten",
    "ui.stacksId.editCredential": "Zugangsdaten bearbeiten",
    "ui.stacksId.enrollment": "Registrierung",
    "ui.stacksId.failedToAddCredential":
      "Zugangsdaten konnten nicht hinzugefügt werden",
    "ui.stacksId.failedToDeleteCredential":
      "Zugangsdaten konnten nicht gelöscht werden",
    "ui.stacksId.failedToLoadRuntimeStatus":
      "Runtime-Status konnte nicht geladen werden",
    "ui.stacksId.failedToLoadStackkitDeployment":
      "StackKit-Deployment konnte nicht geladen werden",
    "ui.stacksId.ingest": "Ingest",
    "ui.stacksId.latestOperationsSnapshot": "Neuester Snapshot der Vorgänge",
    "ui.stacksId.manageCredentialsForThisStackkit":
      "Zugangsdaten für dieses StackKit-Deployment verwalten",
    "ui.stacksId.missingCredentialsDetected": "Fehlende Zugangsdaten erkannt",
    "ui.stacksId.missingSecret": "Fehlendes Secret",
    "ui.stacksId.monitoringEvidence": "Monitoring-Nachweis",
    "ui.stacksId.monthlyRuntime": "Monatliche Runtime",
    "ui.stacksId.monthlyRuntimeActionFailed":
      "Aktion für die monatliche Runtime fehlgeschlagen",
    "ui.stacksId.noCredentialsConfiguredForThis":
      "Für dieses StackKit-Deployment sind keine Zugangsdaten konfiguriert.",
    "ui.stacksId.noLeaseAttached": "Kein Lease verknüpft",
    "ui.stacksId.noStackkitDeploymentIdProvided":
      "Keine StackKit-Deployment-ID angegeben",
    "ui.stacksId.offeringSpec": "{vcpus} vCPU · {memory} GB RAM",
    "ui.stacksId.pageTitle": "{name} – Zugangsdaten | kombify-Techstack",
    "ui.stacksId.query": "Abfrage",
    "ui.stacksId.runtime": "Runtime",
    "ui.stacksId.series": "Serie",
    "ui.stacksId.servicesDeployed.one": "{count} Dienst bereitgestellt",
    "ui.stacksId.servicesDeployed.other": "{count} Dienste bereitgestellt",
    "ui.stacksId.sshInfo": "SSH-Infos",
    "ui.stacksId.sshOff": "SSH aus",
    "ui.stacksId.sshOn": "SSH an",
    "ui.stacksId.stackkitDeploymentServices":
      "Dienste des StackKit-Deployments",
    "ui.stacksId.stackkitService": "StackKit-Dienst",
    "ui.stacksId.start": "Starten",
    "ui.stacksId.status": "Status",
    "ui.stacksId.stop": "Stoppen",
    "ui.stacksId.stopManagedServer": "Verwalteten Server stoppen?",
    "ui.stacksId.storedCredentials": "Gespeicherte Zugangsdaten",
    "ui.stacksId.techstackWillBeginProviderCleanup":
      "Techstack beginnt mit der Provider-Bereinigung für diese verwaltete Runtime.",
    "ui.stacksId.theFollowingServicesNeedCredentials":
      "Für die folgenden Dienste müssen Zugangsdaten konfiguriert werden:",
    "ui.stacksId.theServerShutsDownAnd":
      "Der Server fährt herunter und behält Festplatte, Adresse und Monatstarif. Du kannst ihn jederzeit wieder starten.",
    "ui.stacksId.thisPermanentlyRemovesTheSelected":
      "Dadurch werden die ausgewählten Zugangsdaten dauerhaft entfernt.",
    "ui.stacksId.username": "Benutzername",
    "ui.stacksIdServersNew.addNodeKombifyTechstack":
      "Node hinzufügen | kombify-Techstack",
    "ui.stacksIdServersServerId.activityHistory": "Aktivitätsverlauf",
    "ui.stacksIdServersServerId.addressesDomains": "Adressen & Domains",
    "ui.stacksIdServersServerId.agentId": "Agent-ID",
    "ui.stacksIdServersServerId.architecture": "Architektur",
    "ui.stacksIdServersServerId.backToOperations": "Zurück zu den Vorgängen",
    "ui.stacksIdServersServerId.canonicalServerAccessIsNot":
      "Der kanonische Serverzugriff ist noch nicht verfügbar.",
    "ui.stacksIdServersServerId.checking": "Wird geprüft...",
    "ui.stacksIdServersServerId.checks": "Prüfungen",
    "ui.stacksIdServersServerId.confirmAction": "Aktion bestätigen",
    "ui.stacksIdServersServerId.confirmDecommission":
      "Außerbetriebnahme bestätigen",
    "ui.stacksIdServersServerId.confirmDetach": "Trennen bestätigen",
    "ui.stacksIdServersServerId.couldNotLoadServerAccess":
      "Serverzugriffskontext konnte nicht geladen werden.",
    "ui.stacksIdServersServerId.cpuCores": "CPU-Kerne",
    "ui.stacksIdServersServerId.custodyResolutionFailed":
      "Auflösung der Obhut fehlgeschlagen.",
    "ui.stacksIdServersServerId.decommissionFailed":
      "Außerbetriebnahme fehlgeschlagen.",
    "ui.stacksIdServersServerId.decommissionServer":
      "Server außer Betrieb nehmen",
    "ui.stacksIdServersServerId.desiredListenersDurableReservationsAnd":
      "Gewünschte Listener, dauerhafte Reservierungen und Runtime-Nachweise für diesen Node.",
    "ui.stacksIdServersServerId.detachSelfOwnedServer":
      "Eigenen Server trennen",
    "ui.stacksIdServersServerId.detachServer": "Server trennen",
    "ui.stacksIdServersServerId.detaching": "Wird getrennt...",
    "ui.stacksIdServersServerId.disk": "Festplatte",
    "ui.stacksIdServersServerId.drift": "Drift",
    "ui.stacksIdServersServerId.exposed": "Freigegeben",
    "ui.stacksIdServersServerId.intent": "Intent",
    "ui.stacksIdServersServerId.lastSeen": "Zuletzt gesehen",
    "ui.stacksIdServersServerId.lifecycleActionsBecomeAvailableWhen":
      "Lebenszyklus-Aktionen sind verfügbar, sobald dieser Server verbunden, freigegeben und dem Stack zugewiesen ist.",
    "ui.stacksIdServersServerId.listener": "Listener",
    "ui.stacksIdServersServerId.liveStreamReconnecting":
      "Live-Stream verbindet sich neu…",
    "ui.stacksIdServersServerId.loadingServerDetails":
      "Serverdetails werden geladen",
    "ui.stacksIdServersServerId.logs": "Logs",
    "ui.stacksIdServersServerId.manageSettingsThatAffectThis":
      "Verwalte Einstellungen, die den Lebenszyklus dieses Servers betreffen. Routinemäßige Status- und Dienststeuerung bleibt in den jeweiligen Tabs.",
    "ui.stacksIdServersServerId.managedRuntimeLeaseContext":
      "Kontext des Leases der verwalteten Runtime.",
    "ui.stacksIdServersServerId.memory": "Arbeitsspeicher",
    "ui.stacksIdServersServerId.metadata": "Metadaten",
    "ui.stacksIdServersServerId.noCompilerDeclaredReservationOr":
      "Im neuesten vollständigen Guard-Snapshot war weder eine vom Compiler deklarierte Reservierung noch ein gebundener Runtime-Listener vorhanden.",
    "ui.stacksIdServersServerId.noDesiredListenerIsReserved":
      "Es ist kein gewünschter Listener reserviert, und der Guard konnte seinen Listener-Snapshot nicht abschließen. Runtime-Listener bleiben daher unbekannt.",
    "ui.stacksIdServersServerId.noDesiredListenerIsReserved2":
      "Es ist kein gewünschter Listener reserviert, und der Guard hat für diesen Node noch keinen Listener-Snapshot gemeldet.",
    "ui.stacksIdServersServerId.noHostAddressHasBeen":
      "Es wurde keine Hostadresse gemeldet.",
    "ui.stacksIdServersServerId.noObservedServiceEndpointsHave":
      "Dieser Server hat keine beobachteten Dienst-Endpunkte gemeldet.",
    "ui.stacksIdServersServerId.noPortAllocationsRecorded":
      "Keine Portzuweisungen erfasst",
    "ui.stacksIdServersServerId.noPortEvidenceYet": "Noch keine Port-Nachweise",
    "ui.stacksIdServersServerId.noPreCheckResultIs":
      "Es ist noch kein Vorabprüfungsergebnis erfasst.",
    "ui.stacksIdServersServerId.noServerInstallerOrStackkits":
      "Es sind noch keine Server-, Installer- oder StackKits-Logs erfasst.",
    "ui.stacksIdServersServerId.noServiceDomainsReported":
      "Keine Dienst-Domains gemeldet.",
    "ui.stacksIdServersServerId.noServicePlacementIsRecorded":
      "Für diesen Server ist keine Dienst-Platzierung erfasst.",
    "ui.stacksIdServersServerId.noStackkitDeploymentEvidenceHas":
      "Dieser Server hat keine StackKit-Deployment-Nachweise gemeldet.",
    "ui.stacksIdServersServerId.notDeclared": "Nicht deklariert",
    "ui.stacksIdServersServerId.operatingSystem": "Betriebssystem",
    "ui.stacksIdServersServerId.portAllocations": "Portzuweisungen",
    "ui.stacksIdServersServerId.portEvidenceIsPartial":
      "Port-Nachweise sind unvollständig",
    "ui.stacksIdServersServerId.portInventoryIsNotAvailable":
      "Port-Inventar ist noch nicht verfügbar",
    "ui.stacksIdServersServerId.preChecks": "Vorabprüfungen",
    "ui.stacksIdServersServerId.reconnectFailed":
      "Erneutes Verbinden fehlgeschlagen.",
    "ui.stacksIdServersServerId.reservation": "Reservierung",
    "ui.stacksIdServersServerId.resolveStaleCustodyRecord":
      "Veralteten Obhutseintrag auflösen",
    "ui.stacksIdServersServerId.resolveStaleRecord":
      "Veralteten Eintrag auflösen",
    "ui.stacksIdServersServerId.runtimeEvidenceIsPartialUnknown":
      "Die Runtime-Nachweise sind unvollständig. Unbekannte Zustände bleiben unbekannt, bis der Guard einen vollständigen Listener- oder Exposure-Snapshot meldet.",
    "ui.stacksIdServersServerId.serverCleanupInProgress":
      "Server-Bereinigung läuft",
    "ui.stacksIdServersServerId.serverDetachFailed":
      "Trennen des Servers fehlgeschlagen.",
    "ui.stacksIdServersServerId.serverDetailSections":
      "Abschnitte der Serverdetails",
    "ui.stacksIdServersServerId.serverDetailsKombifyTechstack":
      "Serverdetails | kombify-Techstack",
    "ui.stacksIdServersServerId.serverGenerationDecommissioned":
      "Server-Generation außer Betrieb genommen",
    "ui.stacksIdServersServerId.serverSettings": "Servereinstellungen",
    "ui.stacksIdServersServerId.serviceEndpoints": "Dienst-Endpunkte",
    "ui.stacksIdServersServerId.servicesOnThisServerMay":
      "Dienste auf diesem Server können unerreichbar werden. Die Anfrage gilt erst als abgeschlossen, wenn die Abwesenheit beim Provider verifiziert ist.",
    "ui.stacksIdServersServerId.stackkitActions": "StackKit-Aktionen",
    "ui.stacksIdServersServerId.theAgentIdentityWillBe":
      "Die Agent-Identität wird sofort widerrufen. Der physische Server und sein Provider-Konto bleiben unberührt.",
    "ui.stacksIdServersServerId.theOldProviderGenerationCannot":
      "Die alte Provider-Generation kann nicht erneut gestartet werden. Nutze oben „Neu erstellen“, nachdem die genaue Abwesenheit beim Provider und die Freigabe der Kapazität verifiziert wurden.",
    "ui.stacksIdServersServerId.thisChangesTheSelectedServer":
      "Dies ändert den ausgewählten Server über seinen registrierten StackKit-Agent.",
    "ui.stacksIdServersServerId.thisIsConfiguredStackkitIntent":
      "Dies ist der konfigurierte StackKit-Intent; der Guard hat noch keine passenden Deployment-Nachweise gemeldet.",
    "ui.stacksIdServersServerId.thisOnlyArchivesTheStale":
      "Dies archiviert nur den veralteten Techstack-Obhutseintrag. Beim Provider wird nichts gelöscht.",
    "ui.stacksIdServersServerId.thisServerHasNoManaged":
      "Dieser Server hat keinen verwalteten Provider-Lease. Seine erfasste Obhut berechtigt derzeit weder zur verwalteten Außerbetriebnahme noch zum Trennen eines eigenen Servers.",
    "ui.stacksIdServersServerId.thisServerIsALegacy":
      "Dieser Server ist ein Legacy- oder ungebundener Obhutseintrag. Bestätige, dass die Provider-Ressource bereits entfernt wurde; Techstack archiviert nur diesen Eintrag und ruft keine Provider-Ressource auf oder löscht sie.",
    "ui.stacksIdServersServerId.thisServerIsRegisteredThrough":
      "Dieser Server ist über das Worker-Inventar registriert; Zugriffsaktionen der verwalteten Runtime sind nicht angebunden.",
    "ui.standardBundle.additionalComputeNodeForStackKit":
      "Zusätzlicher Compute-Node für die Platzierung von StackKit-Diensten.",
    "ui.standardBundle.additionalNodeIntendedForStorage":
      "Zusätzlicher Node für speicherintensive Dienste.",
    "ui.standardBundle.addsALoginProtectedPassword":
      "Fügt einen durch Login geschützten Passwort-Tresor hinzu, wenn der Anwendungsfall Vault ausgewählt ist.",
    "ui.standardBundle.authAndDatabaseBackend": "Auth- und Datenbank-Backend",
    "ui.standardBundle.basementKitStandardRelease":
      "Basement-Kit-Standardrelease",
    "ui.standardBundle.collectsTelemetrySignalsForThe":
      "Sammelt Telemetriesignale für die Day-2-Monitoring-Basis.",
    "ui.standardBundle.createsASecureMeshNetwork":
      "Erstellt ein sicheres Mesh-Netzwerk für den Gerätezugriff ohne öffentliche Ports.",
    "ui.standardBundle.defaultIdentityProviderForLogin":
      "Standard-Identitätsanbieter für durch Login geschützte Dienste",
    "ui.standardBundle.directPrivateMesh": "Direktes privates Mesh",
    "ui.standardBundle.expandsToTheImmichServer":
      "Wird zu den Spezifikationen für Immich-Server, Machine-Learning-Worker, Postgres und Redis erweitert.",
    "ui.standardBundle.fileStorageModule": "Dateispeicher-Modul",
    "ui.standardBundle.files": "Dateien",
    "ui.standardBundle.firstCoreNodeWireCompatible":
      "Erster/Core-Node; wire-kompatibel mit main, standalone und control-plane.",
    "ui.standardBundle.foundationNode": "Foundation-Node",
    "ui.standardBundle.germanyEUPrimary": "Deutschland / EU primär",
    "ui.standardBundle.germanyEUSecondary": "Deutschland / EU sekundär",
    "ui.standardBundle.goals": "Ziele",
    "ui.standardBundle.handlesRoutingHTTPSAndService":
      "Übernimmt Routing, HTTPS und Dienst-Einstiegspunkte für den Stack.",
    "ui.standardBundle.localNetworkOnly": "Nur lokales Netzwerk",
    "ui.standardBundle.localOrUserOwnedNode":
      "Lokales oder nutzereigenes Node-Ziel für den ersten Rollout und die Erweiterung.",
    "ui.standardBundle.login": "Anmeldung",
    "ui.standardBundle.managedVPSFoundationForKombify":
      "Verwaltete VPS-Basis für kombify-Cloud-Rollouts.",
    "ui.standardBundle.none": "Keine",
    "ui.standardBundle.optionalBackendCapabilityWhenA":
      "Optionale Backend-Fähigkeit, wenn ein Stack PocketBase-native Auth oder Daten benötigt.",
    "ui.standardBundle.passwordVault": "Passwort-Tresor",
    "ui.standardBundle.photoLibraryWithSupportingDatabase":
      "Fotobibliothek mit unterstützender Datenbank, Cache und ML-Diensten",
    "ui.standardBundle.pocketIDIsTheStandard":
      "Pocket ID ist der standardmäßige externe Identitäts-Head für den StackKit-Zugriff.",
    "ui.standardBundle.reservedForTheFileStorage":
      "Reserviert für das Dateispeicher-Modul, sobald es in der Release-Basis enthalten ist.",
    "ui.standardBundle.reverseProxyAndLoadBalancer":
      "Reverse-Proxy und Load Balancer",
    "ui.standardBundle.standardObservabilityPipeline":
      "Standard-Observability-Pipeline",
    "ui.standardBundle.storageNode": "Storage-Node",
    "ui.standardBundle.tailscaleCompatibleMeshVPN":
      "Tailscale-kompatibles Mesh-VPN",
    "ui.standardBundle.users": "Nutzer",
    "ui.standardBundle.workerNode": "Worker-Node",
    "ui.strata.aRolloutIsInProgress":
      "Auf {rolloutNode} läuft gerade ein Rollout.",
    "ui.strata.activeNote": "aktiv",
    "ui.strata.alertsActive.one": "{count} Warnung ist aktiv.",
    "ui.strata.alertsActive.other": "{count} Warnungen sind aktiv.",
    "ui.strata.backup": "Backup",
    "ui.strata.centre": "Zentrum · {apps} Apps, {system} System",
    "ui.strata.cpuCurve": "{name} CPU in den letzten 24 Stunden",
    "ui.strata.gaugeAria":
      "{name}: CPU {cpu} Prozent, Arbeitsspeicher {ram} Prozent, Festplatte {disk} Prozent, {services} Dienste",
    "ui.strata.job": "Job",
    "ui.strata.noCpuCurve":
      "{name}: keine CPU-Daten für die letzten 24 Stunden",
    "ui.strata.noNodeHasReportedYet": "Noch kein Node hat sich gemeldet.",
    "ui.strata.nodesNeedAttention.one":
      "{count} von {total} Nodes braucht Aufmerksamkeit.",
    "ui.strata.nodesNeedAttention.other":
      "{count} von {total} Nodes brauchen Aufmerksamkeit.",
    "ui.strata.notShown":
      "Nicht angezeigt: {items} konnten nicht gelesen werden.",
    "ui.strata.provisioning": "Bereitstellung",
    "ui.strata.quiet": "ruhig",
    "ui.strata.removal": "Entfernung",
    "ui.strata.restore": "Wiederherstellung",
    "ui.strata.rightNow": "gerade jetzt",
    "ui.strata.rollout": "Rollout",
    "ui.strata.theLastRolloutOnFailed":
      "Der letzte Rollout auf {failureNode} ist fehlgeschlagen.",
    "ui.strata.upgrade": "Upgrade",
    "ui.strataDashboard.alerts": "Warnungen",
    "ui.strataDashboard.cpuSeriesUnavailable": "CPU-Verlauf nicht verfügbar",
    "ui.strataDashboard.devices": "Geräte",
    "ui.strataDashboard.greenBelow75AmberFrom":
      "Grün unter 75 %, Gelb ab 75 %, Rot ab 90 %.",
    "ui.strataDashboard.innerRing": "Innerer Ring",
    "ui.strataDashboard.loadingCpu": "CPU wird geladen…",
    "ui.strataDashboard.middleRing": "Mittlerer Ring",
    "ui.strataDashboard.monitoring": "Monitoring →",
    "ui.strataDashboard.noCpuDataInThe": "Keine CPU-Daten in den letzten 24 h",
    "ui.strataDashboard.nodes": "Nodes",
    "ui.strataDashboard.nothingHappenedNoRolloutsRestores":
      "Nichts passiert: keine Rollouts, Wiederherstellungen, Warnungen oder Ausfälle.",
    "ui.strataDashboard.outerRing": "Äußerer Ring",
    "ui.substrate.expires":
      "Läuft ab: {time}. Der Befehl wird nur hier angezeigt und nicht in Job-Berichten aufbewahrt.",
    "ui.substrateEnrollment.aConnectionCommandCouldNot":
      "Ein Verbindungsbefehl konnte nicht vorbereitet werden.",
    "ui.substrateEnrollment.couldNotPrepareTheConnection":
      "Die Verbindung konnte nicht vorbereitet werden.",
    "ui.substrateEnrollment.generateANewCommand": "Neuen Befehl erzeugen",
    "ui.substrateEnrollment.generateConnectionCommand":
      "Verbindungsbefehl erzeugen",
    "ui.substrateEnrollment.preparing": "Wird vorbereitet...",
    "ui.substrateEnrollment.proxmoxHypervisorConnection":
      "Proxmox-Hypervisor-Verbindung",
    "ui.substrateEnrollment.theHypervisorHostsYourNodes":
      "Der Hypervisor hostet deine Nodes. StackKits laufen in Ubuntu-Gästen. Die Gastverwaltung wird aktiviert, sobald der lokale Proxmox-Client konfiguriert ist und du die Verbindung autorisierst. Bestehende Gäste bleiben außerhalb der Lösch-Obhut.",
    "ui.substrates.theRuntimeDidNotReturn":
      "Die Runtime hat die angeforderten Daten nicht zurückgegeben.",
    "ui.taskStatus.aVerifiedStackKitDeploymentMust":
      "Ein verifiziertes StackKit-Deployment muss für die Standarddienste einen getesteten Wiederherstellungspfad haben.",
    "ui.taskStatus.analyzingYourGoalsToFind":
      "Deine Ziele werden analysiert, um die passendste StackKit-Vorlage zu finden.",
    "ui.taskStatus.applyingSecurityPolicies":
      "Sicherheitsrichtlinien werden angewendet",
    "ui.taskStatus.basedOnYourGoalsThe":
      "Anhand deiner Ziele wählt der Unifier Container aus, legt Ressourcenlimits fest und löst Abhängigkeiten zwischen Diensten auf.",
    "ui.taskStatus.buildingServiceList": "Dienstliste wird erstellt",
    "ui.taskStatus.callingTheStackKitsRuntimeAction":
      "Die StackKits-Runtime-Aktion, die die generierte Cloud-Kit-Spezifikation anwendet, wird aufgerufen.",
    "ui.taskStatus.checkingOpenTofu": "OpenTofu wird geprüft",
    "ui.taskStatus.checkingRolloutTarget": "Rollout-Ziel wird geprüft",
    "ui.taskStatus.checkingTerramate": "Terramate wird geprüft",
    "ui.taskStatus.checkingThatLoginProtectedServices":
      "Es wird geprüft, ob durch Login geschützte Dienste und Monitoring-Signale nach dem Rollout verfügbar sind.",
    "ui.taskStatus.checkingTheTerramateToolchainWhen":
      "Die Terramate-Toolchain wird geprüft, wenn der ausgewählte StackKit-Lebenszyklus sie benötigt.",
    "ui.taskStatus.checkingYourChoices": "Deine Auswahl wird geprüft",
    "ui.taskStatus.collectingServiceMetadataExposedBy":
      "Vom StackKits-Rollout bereitgestellte Dienst-Metadaten werden gesammelt.",
    "ui.taskStatus.combiningUserIntentStackKitDefaults":
      "Nutzerabsicht, StackKit-Standardwerte und Runtime-Informationen werden zur endgültigen Deployment-Spezifikation kombiniert.",
    "ui.taskStatus.compilingEverythingIntoAFinal":
      "Alles wird zu einer endgültigen StackKits-Deployment-Spezifikation kompiliert.",
    "ui.taskStatus.configureStackKitDeployment":
      "StackKit-Deployment konfigurieren",
    "ui.taskStatus.configuringAccessProfilesReverseProxy":
      "Zugriffsprofile, Reverse-Proxy, DNS und internes Netzwerk werden konfiguriert.",
    "ui.taskStatus.configuringAuthentication":
      "Authentifizierung wird konfiguriert",
    "ui.taskStatus.configuringFirewallRulesTLSCertificates":
      "Firewall-Regeln, TLS-Zertifikate und Isolationseinstellungen werden konfiguriert.",
    "ui.taskStatus.configuringNetworkSettings":
      "Netzwerkeinstellungen werden konfiguriert",
    "ui.taskStatus.configuringSingleSignOnUser":
      "Single Sign-on, Benutzerkonten und Zugriffskontrolle werden konfiguriert.",
    "ui.taskStatus.confirmingServicesAndValidatingThe":
      "Dienste werden bestätigt und die Wiederherstellungsübung wird validiert",
    "ui.taskStatus.confirmingThatTheBoundManaged":
      "Es wird bestätigt, dass der gebundene verwaltete VPS von der StackKits-CLI angesprochen werden kann.",
    "ui.taskStatus.confirmingVPSTarget": "VPS-Ziel wird bestätigt",
    "ui.taskStatus.connectingToRuntime":
      "Verbindung zur Runtime wird hergestellt",
    "ui.taskStatus.creatingOrBindingTheSubscription":
      "Der Abo-VM-Lease für diesen StackKit-Rollout wird erstellt oder gebunden.",
    "ui.taskStatus.creatingYourDeploymentSpec":
      "Deine Deployment-Spezifikation wird erstellt",
    "ui.taskStatus.determiningWhichServicesAreNeeded":
      "Es wird ermittelt, welche Dienste benötigt werden, und Best-Practice-Standardwerte werden angewendet.",
    "ui.taskStatus.eachServiceGetsScopedPermissions":
      "Jeder Dienst erhält eingegrenzte Berechtigungen. TLS wird, wo möglich, automatisch aktiviert, und Dienste sind standardmäßig isoliert.",
    "ui.taskStatus.ensuringTheDeploymentTargetSatisfies":
      "Es wird sichergestellt, dass das Deployment-Ziel die Anforderungen der StackKit-Runtime erfüllt.",
    "ui.taskStatus.findingTheBestStackKitFor":
      "Das beste StackKit für dich wird gesucht",
    "ui.taskStatus.generateDeploymentArtifacts":
      "Deployment-Artefakte generieren",
    "ui.taskStatus.generatingDeploymentSpec":
      "Deployment-Spezifikation wird generiert",
    "ui.taskStatus.generatingStackKitIaC": "StackKit-IaC wird generiert",
    "ui.taskStatus.generatingUnifiedSpec":
      "Vereinheitlichte Spezifikation wird generiert",
    "ui.taskStatus.identifyingServicesBestPractices":
      "Dienste & Best Practices werden ermittelt",
    "ui.taskStatus.ifAptOrUnattendedUpgrades":
      "Wenn apt oder unattended-upgrades die Paketinstallation blockieren, hält Techstack die gebundene VM sichtbar und zeigt die gesammelte Diagnose.",
    "ui.taskStatus.installingAndCheckingDocker":
      "Docker wird installiert und geprüft",
    "ui.taskStatus.installingOrValidatingTheDocker":
      "Die Docker-Runtime, die von den ausgewählten Diensten genutzt wird, wird installiert oder validiert.",
    "ui.taskStatus.loadingThePersistedIntentAnd":
      "Der gespeicherte Intent wird geladen; es wird gewartet, bis das verwaltete VPS-Ziel erreichbar ist.",
    "ui.taskStatus.managedKombifyCloudRolloutsUse":
      "Verwaltete kombify-Cloud-Rollouts nutzen das VM-Lease-Ziel; nutzereigene Rollout-Ziele erfordern freigegebene Worker.",
    "ui.taskStatus.matchingAStackKit": "Ein StackKit wird zugeordnet",
    "ui.taskStatus.networkSettingsAreDerivedFrom":
      "Die Netzwerkeinstellungen werden aus deinem Zugriffsmodus abgeleitet. „Nur lokal“ nutzt internes Docker-Netzwerk; Fernzugriff fügt das zur Lane passende private Mesh oder die verwaltete Edge-Route hinzu.",
    "ui.taskStatus.onceThisSucceedsTheNode":
      "Sobald dies gelingt, bleibt die Node-Projektion in Techstack sichtbar, auch wenn Vorbereitung oder Rollout später fehlschlagen.",
    "ui.taskStatus.opentofuReadinessIsPartOf":
      "Die OpenTofu-Bereitschaft ist Teil des Vorbereitungsvertrags der StackKits-CLI, kein separater, von Techstack verantworteter Bootstrap-Pfad.",
    "ui.taskStatus.persistingConfiguration": "Konfiguration wird gespeichert",
    "ui.taskStatus.persistingRolloutSpec":
      "Rollout-Spezifikation wird gespeichert",
    "ui.taskStatus.preparingDocker": "Docker wird vorbereitet",
    "ui.taskStatus.preparingNodeRegistration":
      "Node-Registrierung wird vorbereitet",
    "ui.taskStatus.preparingOpenTelemetryHandoffDataFor":
      "OpenTelemetry-Übergabedaten für Monitoring und Betrieb werden vorbereitet.",
    "ui.taskStatus.preparingStackKitsRuntime":
      "StackKits-Runtime wird vorbereitet",
    "ui.taskStatus.preparingTelemetry": "Telemetrie wird vorbereitet",
    "ui.taskStatus.preparingTheRuntimeMetadataUsed":
      "Die Runtime-Metadaten für Monitoring, Betrieb und den Runtime Intelligence Layer werden vorbereitet.",
    "ui.taskStatus.preparingToolsApplyingTheStackKit":
      "Tools werden vorbereitet, das StackKit angewendet und Dienste gelesen",
    "ui.taskStatus.provisionManagedRuntime": "Verwaltete Runtime bereitstellen",
    "ui.taskStatus.readingServiceInventory": "Dienst-Inventar wird gelesen",
    "ui.taskStatus.renderingStackKitsArtifactsAfterVPS":
      "StackKits-Artefakte werden nach der VPS-Bereitschaft gerendert",
    "ui.taskStatus.renderingTheStackKitInfrastructureFiles":
      "Die vom Rollout-Adapter benötigten StackKit-Infrastrukturdateien werden gerendert.",
    "ui.taskStatus.requestingManagedCloudNode":
      "Verwalteter Cloud-Node wird angefordert",
    "ui.taskStatus.requestingManagedNode": "Verwalteter Node wird angefordert",
    "ui.taskStatus.reservingTheSubscriptionVMConnecting":
      "Abo-VM wird reserviert, Runtime verbunden und Telemetrie vorbereitet",
    "ui.taskStatus.rollOutCloudKit": "Cloud Kit ausrollen",
    "ui.taskStatus.rollingOutCloudKit": "Cloud Kit wird ausgerollt",
    "ui.taskStatus.runningRestoreDrill": "Wiederherstellungsübung läuft",
    "ui.taskStatus.runningSimulatedUpdateGate":
      "Simulierte Update-Schranke läuft",
    "ui.taskStatus.runningSimulationGate": "Simulationsschranke läuft",
    "ui.taskStatus.runningTheStackKitsCLIPrepare":
      "Der Prepare-Vertrag der StackKits-CLI wird auf dem verwalteten VPS ausgeführt.",
    "ui.taskStatus.savingUnifiedSpecYamlSo":
      "unified-spec.yaml wird gespeichert, damit der Rollout reproduzierbar und nachvollziehbar ist.",
    "ui.taskStatus.savingYourChoicesToThe":
      "Deine Auswahl wird in der Datenbank gespeichert, damit sie beim Deployment referenziert werden kann.",
    "ui.taskStatus.savingYourConfiguration":
      "Deine Konfiguration wird gespeichert",
    "ui.taskStatus.settingUpAuthentication":
      "Authentifizierung wird eingerichtet",
    "ui.taskStatus.settingUpNetworking": "Netzwerk wird eingerichtet",
    "ui.taskStatus.settingUpSecurityConfiguration":
      "Sicherheitskonfiguration wird eingerichtet",
    "ui.taskStatus.stackkitsAreCuratedInfrastructureTemplates":
      "StackKits sind kuratierte Infrastrukturvorlagen. Das System wählt eine aus, die deine ausgewählten Funktionen mit minimalem Overhead abdeckt.",
    "ui.taskStatus.startingTelemetryHandoff":
      "Telemetrie-Übergabe wird gestartet",
    "ui.taskStatus.techstackConsumesStackKitArtifactsHere":
      "Techstack verarbeitet hier StackKit-Artefakte; StackKits bleibt für deren Anwendung verantwortlich.",
    "ui.taskStatus.techstackRecordsTheManagedTarget":
      "Techstack erfasst das verwaltete Ziel und bereitet die Orchestrierungs-Übergabe vor, bevor StackKits den Cloud-Kit-Rollout durchführt.",
    "ui.taskStatus.terramateReadinessBelongsToStackKits":
      "Die Terramate-Bereitschaft gehört zur Lebenszyklus-Vorbereitung von StackKits; Techstack benötigt sie für den initialen verwalteten VPS-Lease nicht.",
    "ui.taskStatus.theDashboardCanShowManaged":
      "Das Dashboard kann verwaltete Dienste anzeigen, sobald StackKits sie bereitstellt, während die spätere Verifizierung weiterläuft.",
    "ui.taskStatus.theFirstRolloutRecordsThe":
      "Der erste Rollout erfasst den Runtime-Kontext, den spätere Dienstkarten, Metriken und RIL-Workflows nutzen.",
    "ui.taskStatus.theLeaseCapturesRuntimeState":
      "Der Lease erfasst den Runtime-Zustand, den Abrechnungsrhythmus und den verwalteten Provider, der das Cloud Kit hosten wird.",
    "ui.taskStatus.thePersistedSpecLinksBack":
      "Die gespeicherte Spezifikation verweist auf die Anforderungsdatei und die ursprüngliche StackKit-Deployment-Anfrage zurück.",
    "ui.taskStatus.theSimulationGateProtectsThe":
      "Die Simulationsschranke schützt den ersten Rollout und spätere Update-Abläufe vor unsicheren Änderungen.",
    "ui.taskStatus.theSpecContainsAllConfiguration":
      "Die Spezifikation enthält die gesamte Konfiguration, die zum Deployment deines Homelabs nötig ist. Sie lässt sich versionieren und auf jedem kompatiblen Node reproduzieren.",
    "ui.taskStatus.theUnifiedSpecIsThe":
      "Die vereinheitlichte Spezifikation ist die kanonische Eingabe für StackKits und die Runtime-Verifizierung.",
    "ui.taskStatus.thisChecksFeatureSelectionsAccess":
      "Dabei werden Funktionsauswahl, Zugriffsmodi, Benutzerkonfiguration und Authentifizierungseinstellungen auf Konsistenz geprüft.",
    "ui.taskStatus.thisChecksThatThePersisted":
      "Dabei wird geprüft, ob die gespeicherte StackKit-Deployment-Spezifikation und requirements-spec.yaml noch übereinstimmen; dann wird bestätigt, dass der verwaltete VM-Lease einen Runtime-SSH-Host oder eine öffentliche IP bereitstellt, bevor die StackKits-Artefaktgenerierung startet.",
    "ui.taskStatus.thisIsThePointWhere":
      "An diesem Punkt werden die ausgewählten Dienste auf dem Runtime-Ziel installiert und konfiguriert.",
    "ui.taskStatus.thisNonInteractivePrepStep":
      "Dieser nicht interaktive Vorbereitungsschritt installiert und prüft die Tools, die StackKits benötigt, bevor das Cloud Kit angewendet wird.",
    "ui.taskStatus.thisStackKitDeploymentConfigurationIs":
      "Diese StackKit-Deployment-Konfiguration wird sicher gespeichert und kann später im Dashboard exportiert oder geändert werden.",
    "ui.taskStatus.validatingChoicesAndBuildingThe":
      "Auswahl wird validiert und Deployment-Spezifikation erstellt",
    "ui.taskStatus.validatingConfiguration": "Konfiguration wird validiert",
    "ui.taskStatus.validatingTheBackupAndRestore":
      "Backup- und Wiederherstellungspfad werden validiert, bevor das StackKit-Deployment als verifiziert markiert wird.",
    "ui.taskStatus.validatingTheUpdatePathBefore":
      "Der Update-Pfad wird validiert, bevor der Rollout auf die verwaltete Runtime angewendet wird.",
    "ui.taskStatus.verificationConfirmsThatTheStackKit":
      "Die Verifizierung bestätigt, dass das StackKit-Deployment nutzbar ist, nicht nur, dass Dateien generiert wurden.",
    "ui.taskStatus.verifyRollout": "Rollout verifizieren",
    "ui.taskStatus.verifyingLoginProtectedServices":
      "Durch Login geschützte Dienste werden verifiziert",
    "ui.taskStatus.verifyingServices": "Dienste werden verifiziert",
    "ui.taskStatus.verifyingThatAllSelectedOptions":
      "Es wird geprüft, ob alle ausgewählten Optionen gültig und miteinander kompatibel sind.",
    "ui.taskStatus.verifyingTheInfrastructureToolchainNeeded":
      "Die von StackKits benötigte Infrastruktur-Toolchain wird verifiziert.",
    "ui.taskStatus.yourChosenAuthMethodIs":
      "Deine gewählte Auth-Methode wird auf alle Dienste angewendet. Mehrbenutzer-Setups erhalten automatisch gruppenbasierte Berechtigungen.",
    "ui.taskUpdates.anUnexpectedErrorOccurred":
      "Ein unerwarteter Fehler ist aufgetreten",
    "ui.techstackBrandLogo.kombifyTechstack": "kombify Techstack",
    "ui.terminal.title": "Terminal · {name}",
    "ui.time.daysHours": "{days} T {hours} Std",
    "ui.time.hours": "{count} Std",
    "ui.time.hoursAgo": "vor {count} Std",
    "ui.time.hoursMinutes": "{hours} Std {minutes}",
    "ui.time.justNow": "gerade eben",
    "ui.time.minutes": "{count} Min",
    "ui.time.minutesAgo": "vor {count} Min",
    "ui.time.minutesShort": "{count} Min",
    "ui.time.notReported": "nicht gemeldet",
    "ui.time.seconds": "{count} Sek",
    "ui.types.mainController": "Hauptcontroller",
    "ui.types.probing": "Wird sondiert...",
    "ui.types.routerGateway": "Router/Gateway",
    "ui.types.scanning": "Wird gescannt...",
    "ui.types.skipped": "Übersprungen",
    "ui.types.unknown": "Unbekannt",
    "ui.types.utility": "Hilfsgerät",
    "ui.userMenu.version": "Version {version}",
    "ui.wallet.accessControl": "Zugriffskontrolle",
    "ui.wallet.accessControls": "Zugriffskontrollen",
    "ui.wallet.accessEntry": "Zugriffseintrag",
    "ui.wallet.addAccess": "+ Zugriff hinzufügen",
    "ui.wallet.addBreakGlassCredentialsFallback":
      "Füge hier Break-glass-Zugangsdaten, Fallback-Material oder nur einsehbare Secrets hinzu.",
    "ui.wallet.addItem": "{label} hinzufügen",
    "ui.wallet.addTool": "+ Tool hinzufügen",
    "ui.wallet.allTypes": "Alle Typen",
    "ui.wallet.apiKeys": "API-Schlüssel",
    "ui.wallet.applicationAdministratorWithFullHomelab":
      "Anwendungsadministrator mit vollständiger Homelab-Verwaltung",
    "ui.wallet.auto": "Auto",
    "ui.wallet.autoGenerated": "Automatisch generiert",
    "ui.wallet.breakGlassMaterialCredentialsAnd":
      "Break-glass-Material, Zugangsdaten und nur einsehbare Secrets bleiben im Wiederherstellungsbereich.",
    "ui.wallet.breakGlassMaterialRevealOnly":
      "Break-glass-Material, nur einsehbare Zugangsdaten und Fallback-Secrets erscheinen hier.",
    "ui.wallet.browserExtensionTriggeredCheckFor":
      "Browser-Erweiterung ausgelöst! Achte auf die Speicher-Abfrage.",
    "ui.wallet.certificates": "Zertifikate",
    "ui.wallet.clearFilters": "Filter zurücksetzen",
    "ui.wallet.clearWalletSearch": "Wallet-Suche löschen",
    "ui.wallet.collectionEditing": "Sammlungen bearbeiten",
    "ui.wallet.confirmCredentialReveal": "Anzeigen der Zugangsdaten bestätigen",
    "ui.wallet.considerRotating": "{names} – erwäge, sofort zu rotieren.",
    "ui.wallet.context": "Kontext",
    "ui.wallet.copyRoleEmail": "E-Mail für {role} kopieren",
    "ui.wallet.copyRoleSecret": "Secret für {role} kopieren",
    "ui.wallet.couldNotTriggerBrowserExtension":
      "Browser-Erweiterung konnte nicht ausgelöst werden.",
    "ui.wallet.currentPassword": "Aktuelles Passwort",
    "ui.wallet.databaseManagement": "Datenbankverwaltung",
    "ui.wallet.daysLeft": "noch {days} T.",
    "ui.wallet.daysShort": "{days} T.",
    "ui.wallet.deleteWalletEntry": "Wallet-Eintrag löschen?",
    "ui.wallet.developerAccessForTestingAnd":
      "Entwicklerzugriff für Tests und Entwicklung",
    "ui.wallet.discover": "Entdecken",
    "ui.wallet.discoverServiceCredentials": "Dienst-Zugangsdaten entdecken",
    "ui.wallet.dismiss": "Schließen",
    "ui.wallet.editItem": "{label} bearbeiten",
    "ui.wallet.enterNewSecretValue": "Neuen Secret-Wert eingeben",
    "ui.wallet.enterYourCurrentLocalTechstack":
      "Gib dein aktuelles lokales Techstack-Passwort ein, um diesen Wallet-Eintrag anzuzeigen.",
    "ui.wallet.expired": "Abgelaufen",
    "ui.wallet.expiredCredentials.one": "{count} abgelaufene Zugangsdaten",
    "ui.wallet.expiredCredentials.other": "{count} abgelaufene Zugangsdaten",
    "ui.wallet.expires": "Läuft ab",
    "ui.wallet.expiringCredentials.one": "{count} Zugangsdaten laufen bald ab",
    "ui.wallet.expiringCredentials.other":
      "{count} Zugangsdaten laufen bald ab",
    "ui.wallet.extensionTriggered": "Erweiterung ausgelöst!",
    "ui.wallet.freshKombifyCloudReAuthentication":
      "Vor dem Anzeigen von Wallet-Material ist eine frische erneute Authentifizierung bei kombify Cloud erforderlich.",
    "ui.wallet.generateSshKey": "SSH-Schlüssel generieren",
    "ui.wallet.hide": "Verbergen",
    "ui.wallet.hideRoleSecret": "Secret für {role} verbergen",
    "ui.wallet.homelabManagement": "Homelab-Verwaltung",
    "ui.wallet.humanUsersAdminIdentitiesAnd":
      "Menschliche Nutzer, Admin-Identitäten und systemeigene Anmeldeoberflächen befinden sich hier als operative Zugriffsebene.",
    "ui.wallet.identity": "Identität",
    "ui.wallet.importExport": "Import / Export",
    "ui.wallet.kombifyTechstackAdmin": "kombify-Techstack-Admin",
    "ui.wallet.kombifyTechstackDeveloper": "kombify-Techstack-Entwickler",
    "ui.wallet.launchReadyToolsAndUrl":
      "Startbereite Tools und URL-basierte Diensteinträge erscheinen hier, sobald Wallet zum zentralen Ausgangspunkt wird.",
    "ui.wallet.localBackendAdmin": "Lokaler Backend-Admin",
    "ui.wallet.localBackendSuperuser": "Lokaler Backend-Superuser",
    "ui.wallet.logViewing": "Logs ansehen",
    "ui.wallet.manage": "Verwalten",
    "ui.wallet.managedUser": "Verwalteter Nutzer",
    "ui.wallet.managedUsers": "Verwaltete Nutzer",
    "ui.wallet.newSecret": "Neues Secret",
    "ui.wallet.noMatchingRecoveryItems":
      "Keine passenden Wiederherstellungseinträge",
    "ui.wallet.noRecoveryItemsInThis":
      "Keine Wiederherstellungseinträge in dieser Ansicht",
    "ui.wallet.noRecoveryItemsYet": "Noch keine Wiederherstellungseinträge",
    "ui.wallet.noToolsYet": "Noch keine Tools",
    "ui.wallet.notSet": "Nicht festgelegt",
    "ui.wallet.oauthTokens": "OAuth-Tokens",
    "ui.wallet.other": "Sonstige",
    "ui.wallet.passwords": "Passwörter",
    "ui.wallet.quickSync": "Quick-Sync",
    "ui.wallet.quickSyncFailedTryCopying":
      "Quick-Sync fehlgeschlagen. Versuche, manuell zu kopieren.",
    "ui.wallet.quickSyncToPasswordManager":
      "Quick-Sync mit dem Passwortmanager",
    "ui.wallet.recovery": "+ Wiederherstellung",
    "ui.wallet.recoveryItem": "Wiederherstellungseintrag",
    "ui.wallet.recoveryItems": "Wiederherstellungseinträge",
    "ui.wallet.reset": "Zurücksetzen",
    "ui.wallet.reveal": "Anzeigen",
    "ui.wallet.revealRoleSecret": "Secret für {role} anzeigen",
    "ui.wallet.rotateCredential": "Zugangsdaten rotieren",
    "ui.wallet.rotating": "Wird rotiert:",
    "ui.wallet.rotating2": "Wird rotiert...",
    "ui.wallet.save": "Speichern",
    "ui.wallet.saveToExternalPasswordManager":
      "In externem Passwortmanager speichern (1Password, Bitwarden usw.)",
    "ui.wallet.searchCredentials": "Zugangsdaten suchen...",
    "ui.wallet.secret": "Secret",
    "ui.wallet.selfHostedCompatibilityAdminAccess":
      "Admin-Zugriff für Self-Hosted-Kompatibilität",
    "ui.wallet.show": "Anzeigen",
    "ui.wallet.simulationAccess": "Simulationszugriff",
    "ui.wallet.sshKeys": "SSH-Schlüssel",
    "ui.wallet.storedCount": "{count} gespeichert",
    "ui.wallet.syncing": "Wird synchronisiert...",
    "ui.wallet.systemSettings": "Systemeinstellungen",
    "ui.wallet.systemUsers": "Systemnutzer",
    "ui.wallet.thisPermanentlyRemovesTheSelected":
      "Dadurch wird der ausgewählte Wallet-Eintrag dauerhaft entfernt.",
    "ui.wallet.thisWillUpdateTheSecret":
      "Dadurch wird das Secret aktualisiert und das Rotationsdatum zur Nachverfolgung erfasst.",
    "ui.wallet.tool": "Tool",
    "ui.wallet.toolSurface": "Tool-Oberfläche",
    "ui.wallet.tools": "Tools",
    "ui.wallet.triggering": "Wird ausgelöst...",
    "ui.wallet.tryAdjustingYourSearchQuery":
      "Passe deine Suchanfrage oder den Filter für nur einsehbare Einträge an.",
    "ui.wallet.user": "Nutzer",
    "ui.wallet.userManagement": "Nutzerverwaltung",
    "ui.wallet.walletSections": "Wallet-Bereiche",
    "ui.wallet.walletShouldBeTheFirst":
      "Wallet soll der erste Ort sein, um Tools, Dienste und operative Oberflächen zu starten, ohne in Anzeige-Abläufe zu wechseln.",
    "ui.wallet.walletStaysSplitIntoLaunch":
      "Wallet bleibt in Startoberflächen, operativen Zugriff und reines Wiederherstellungsmaterial aufgeteilt.",
    "ui.wallet.youDonTHavePermission":
      "Du hast keine Berechtigung, Wallet-Einträge hinzuzufügen.",
    "ui.wallet.youDonTHavePermission2":
      "Du hast keine Berechtigung, diese Zugangsdaten zu löschen.",
    "ui.windowsOnboarding.serverConnection": "Serververbindung",
    "ui.wizardPreviewController.recommendationPreviewIsTemporarilyUnavailable":
      "Die Empfehlungsvorschau ist vorübergehend nicht verfügbar.",
    "ui.wizardRunRequest.selectAConnectedHypervisorAnd":
      "Wähle einen verbundenen Hypervisor und gültige Ubuntu-Gast-Ressourcen aus.",
    "ui.wizardRunRequest.selectSeparateHomeAssistantOS":
      "Wähle separate Gast-Ressourcen für Home Assistant OS aus.",
    "ui.wizardRuns.thisRunHasNoRollout":
      "Dieser Lauf hat keinen Rollout, der abgebrochen werden könnte.",
    "ui.worker.registryLink": "Worker-Registry-Link",
    "ui.worker.registryUrlError":
      "Registry-URL konnte nicht automatisch ermittelt werden: {error}",
    "ui.worker.runInstallMany":
      "Führe den Installationsbefehl oben auf mindestens {count} Nodes aus und warte dann, bis Betriebsnachweise die Verbindung bestätigen.",
    "ui.worker.runInstallOne":
      "Führe den Installationsbefehl oben auf deinem Node aus und warte dann, bis Betriebsnachweise die Verbindung bestätigen.",
    "ui.worker.verifiedConnected": "{count} verifiziert verbunden",
    "ui.worker.waitingVerified":
      "Warte auf verifizierte Worker ({connected}/{required})",
    "ui.workerRegistrationCard.installCommand": "Installationsbefehl:",
    "ui.workerRegistrationCard.installCommandForNewWorkers":
      "Installationsbefehl für neue Worker:",
    "ui.workerRegistrationCard.nextStep": "Nächster Schritt:",
    "ui.workerRegistrationCard.operationsEvidenceIsRequiredBefore":
      "Betriebsnachweise sind erforderlich, bevor verbundene Nodes bestätigt werden können.",
    "ui.workerRegistrationCard.workersConnectedVerified":
      "Verbundene Worker (verifiziert)",
  },
};
