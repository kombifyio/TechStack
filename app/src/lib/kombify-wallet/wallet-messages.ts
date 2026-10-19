// Customer-visible strings of the kombify Wallet surfaces in Techstack (hosted
// mode): the Wallet page, its dialogs, the server-access picker and the
// permission prompts. Most wording is shared with kombify Cloud's Wallet
// settings so one Wallet reads the same everywhere. Merged into the main
// dictionary by i18n.ts for every locale.
export const walletMessages = {
  en: {
    "kwallet.denied.step_up_required":
      "A fresh second factor is needed. Confirm it and try again.",
    "kwallet.step_up.body":
      "Revealing a value or signing a certificate needs your second factor. Confirm it to continue.",
    "kwallet.step_up.body_minutes":
      "Revealing a value or signing a certificate needs your second factor from the last {minutes} minutes. Confirm it to continue.",
    "kwallet.step_up.confirm": "Confirm with second factor",
    "kwallet.step_up.unavailable":
      "Confirming a second factor is not possible in this window. Open Techstack in its own browser tab and try again.",
    "kwallet.step_up.working": "Opening sign-in…",
    "kwallet.action.delete": "Delete",
    "kwallet.action.delete_named": "Delete {name}",
    "kwallet.action.new_version": "New version",
    "kwallet.action.new_version_named": "New version of {name}",
    "kwallet.action.reveal": "Reveal",
    "kwallet.action.reveal_named": "Reveal {name}",
    "kwallet.action.revoke": "Revoke",
    "kwallet.action.revoke_named": "Revoke {name}",
    "kwallet.action.settings": "Settings",
    "kwallet.action.settings_named": "Settings of {name}",
    "kwallet.action.sign": "Sign certificate",
    "kwallet.action.sign_named": "Sign a certificate with {name}",
    "kwallet.add": "Add item",
    "kwallet.api.wallet_create_right_missing":
      "Techstack needs your permission to save credentials in your kombify Wallet. Allow it, then try again.",
    "kwallet.api.wallet_item_credential_mismatch":
      "This Wallet item does not hold the credential for the chosen sign-in method (SSH key or password). Choose another item or change the method.",
    "kwallet.api.wallet_item_host_mismatch":
      "The host in this Wallet item is not the host of this server. Choose an item for this host or create a new one.",
    "kwallet.api.wallet_item_invalid":
      "That is not a valid Wallet item. Choose one from the list.",
    "kwallet.api.wallet_item_no_public_key":
      "This server access holds no public key to install. Add the key's public line to the item, or use a key pair generated in your Wallet.",
    "kwallet.api.wallet_item_not_found":
      "This item is no longer in your Wallet. Choose another one.",
    "kwallet.api.wallet_item_not_node_access":
      "This item is not server access. Choose a server access item.",
    "kwallet.api.wallet_item_required":
      "Choose a server access item from your kombify Wallet. Techstack does not accept passwords or key labels here.",
    "kwallet.api.wallet_item_unsupported":
      "This installation does not use the kombify Wallet, so a Wallet item cannot be used here.",
    "kwallet.api.wallet_item_unusable":
      "This Wallet item was revoked or deleted and cannot be used. Choose another one.",
    "kwallet.api.wallet_owner_ineligible":
      "Your account cannot use the kombify Wallet with Techstack yet.",
    "kwallet.api.wallet_response_invalid":
      "The kombify Wallet answered in a way Techstack cannot use. Try again, and report it if it persists.",
    "kwallet.api.wallet_right_missing":
      "Techstack is not allowed to use this Wallet item yet. Allow it in your Wallet, then try again.",
    "kwallet.api.wallet_right_missing_use":
      "Techstack may not use this Wallet item yet. Allow it, then try again.",
    "kwallet.api.wallet_self_hosted_only":
      "This action is only available on a self-hosted Techstack.",
    "kwallet.api.wallet_unavailable":
      "The kombify Wallet is temporarily unavailable. Try again shortly.",
    "kwallet.auth.password": "Password",
    "kwallet.auth.private_key": "SSH private key",
    "kwallet.badge.use_only": "Use only",
    "kwallet.cancel": "Cancel",
    "kwallet.close": "Close",
    "kwallet.view.label": "View",
    "kwallet.view.mark": "Icons",
    "kwallet.view.tile": "Tiles",
    "kwallet.view.row": "List",
    "kwallet.view.card": "Cards",
    "kwallet.detail.facts": "Details",
    "kwallet.detail.item": "Item",
    "kwallet.detail.use": "Use",
    "kwallet.detail.history": "History",
    "kwallet.detail.item_missing":
      "This item is not in your Wallet. It may have been deleted, or it belongs to another account.",
    "kwallet.fact.version": "Version",
    "kwallet.fact.use_targets": "Use targets",
    "kwallet.fact.none": "None",
    "kwallet.fact.created": "Created",
    "kwallet.fact.last_release": "Last released",
    "kwallet.fact.releases": "Releases since this version",
    "kwallet.tag.rotate": "Rotate",
    "kwallet.tag.no_policy": "No sign policy",
    "kwallet.preset.access": "Access: logins and server access",
    "kwallet.preset.access_hint":
      "Logins, server access and SSH certificate authorities: what lets people and Techstack into your stacks' servers and apps.",
    "kwallet.preset.recovery": "Recovery: secrets and configurations",
    "kwallet.preset.recovery_hint":
      "Secrets and configurations: where recovery material and break-glass credentials belong.",
    "kwallet.col.scope": "Scope",
    "kwallet.col.state": "State",
    "kwallet.col.techstack": "Techstack",
    "kwallet.col.type": "Type",
    "kwallet.col.updated": "Updated",
    "kwallet.create.description":
      "The value is sealed in this browser and stored encrypted in the Wallet. Techstack does not keep it; only the access you grant can reveal or use it.",
    "kwallet.create.title": "Add to your Wallet",
    "kwallet.creator.hint":
      "Techstack then adds the credentials it creates for your servers, such as a generated access key. It only ever uses the items you assign to it.",
    "kwallet.creator.label":
      "Allow Techstack to save credentials in your Wallet",
    "kwallet.delete.body":
      "The value and all of its versions are erased. This cannot be undone.",
    "kwallet.delete.confirm": "Delete permanently",
    "kwallet.delete.title": "Delete {name}?",
    "kwallet.denied.auth_required": "Sign in again to reach your Wallet.",
    "kwallet.denied.capture_invalid":
      "The Wallet refused the encrypted value. Try again.",
    "kwallet.denied.capture_key_untrusted":
      "The Wallet's encryption key could not be verified. Nothing was sent.",
    "kwallet.denied.conflict":
      "The item changed in the meantime. Reload the list and try again.",
    "kwallet.denied.generic": "The Wallet refused the request.",
    "kwallet.denied.item_action_unsupported":
      "This item does not support that action. An SSH authority is never revealed or given a new version, and only an SSH authority signs.",
    "kwallet.denied.item_denied": "This item was revoked or deleted.",
    "kwallet.denied.item_not_active":
      "This item was revoked or deleted. You can withdraw product access from it, but not grant it.",
    "kwallet.denied.item_not_owned":
      "This item is not in your Wallet, so product access cannot be changed. Reload and try again.",
    "kwallet.denied.item_unknown": "This item is no longer in your Wallet.",
    "kwallet.denied.name_conflict":
      "A variable or configuration with this name already exists in this scope.",
    "kwallet.denied.owner_required":
      "Only you, signed in yourself, can manage your Wallet. Agents and delegated sessions cannot.",
    "kwallet.denied.owner_unknown":
      "Your sign-in does not name a Wallet owner. Sign out and sign in again.",
    "kwallet.denied.replayed": "This change was already sent. Try again.",
    "kwallet.denied.reveal_mismatch":
      "The revealed value did not match this request and was discarded.",
    "kwallet.denied.shredded": "This Wallet was erased.",
    "kwallet.denied.sign_mismatch":
      "The Wallet's answer did not match your request and was discarded.",
    "kwallet.denied.sign_outside_policy":
      "The certificate is outside this authority's sign policy. Check the principals, the lifetime and the source address.",
    "kwallet.denied.sign_policy_invalid":
      "The Wallet refused this sign policy. Check the principals and the lifetimes and try again.",
    "kwallet.denied.sign_policy_missing":
      "This authority has no sign policy yet, so it signs nothing. Set one in its settings.",
    "kwallet.denied.ssh_ca_invalid":
      "The authority's key is not a usable unencrypted Ed25519 OpenSSH key. Import a valid key, or let the Wallet create one.",
    "kwallet.denied.subject_key_invalid":
      "The Wallet cannot sign this public key. Use an Ed25519, ECDSA P-256 or RSA key of 2048 bits or more.",
    "kwallet.denied.too_large": "The value is too large for the Wallet.",
    "kwallet.denied.transport":
      "This browser could not encrypt or open the value. Reload the page and try again.",
    "kwallet.denied.unavailable":
      "The Wallet is temporarily unavailable. Try again shortly.",
    "kwallet.denied.value_use_only":
      "This item's value is use only and cannot be revealed.",
    "kwallet.empty_body":
      "Add an API key, a login, a variable, a configuration or server access to keep it in one place.",
    "kwallet.empty_body_stack":
      "No credentials are stored for this stack yet. Add one to keep it in your Wallet, where Techstack can use it for this stack.",
    "kwallet.empty_title": "Your Wallet is empty",
    "kwallet.error.ca_key_encrypted":
      "This key is protected by a passphrase. Remove the passphrase first, or let the Wallet create a new authority.",
    "kwallet.error.ca_key_invalid":
      "This is not an OpenSSH Ed25519 private key. Paste the whole key, including the BEGIN and END lines.",
    "kwallet.error.configuration_name_invalid":
      "Use up to 128 lowercase letters, digits, dots, hyphens or underscores, starting with a letter or digit.",
    "kwallet.error.description_invalid":
      "Use up to 500 characters, without spaces at the start or end.",
    "kwallet.error.host_invalid": "Enter a host without spaces.",
    "kwallet.error.keygen_unavailable":
      "This browser cannot generate an Ed25519 key. Paste an existing key instead.",
    "kwallet.error.name_invalid":
      "Use 1 to 120 characters, without spaces at the start or end.",
    "kwallet.error.port_invalid":
      "Enter a port from 1 to 65535, or leave it empty for 22.",
    "kwallet.error.public_key_invalid":
      "Enter one OpenSSH public key line that starts with ssh-ed25519, ecdsa-sha2- or ssh-rsa.",
    "kwallet.error.required": "Fill in this field.",
    "kwallet.error.segment_invalid":
      "Use letters, digits, dots, dashes or underscores, up to 64 characters.",
    "kwallet.error.value_too_large": "The value is larger than 64 KiB.",
    "kwallet.error.variable_name_invalid":
      "Use up to 128 capital letters, digits or underscores, not starting with a digit.",
    "kwallet.field.auth": "Sign-in method",
    "kwallet.field.configuration_name": "Configuration name",
    "kwallet.field.configuration_name_hint":
      "What your tools look it up by, such as app.env: lowercase letters, digits, dots, hyphens and underscores, starting with a letter or digit. Unique within the scope.",
    "kwallet.field.description": "Description (optional)",
    "kwallet.field.document": "Document",
    "kwallet.field.environment": "Environment",
    "kwallet.field.host": "Host",
    "kwallet.field.host_hint":
      "A host name or IP address, with a port if it is not 22.",
    "kwallet.field.name": "Name",
    "kwallet.field.name_hint":
      "Shown in lists. Never put the secret itself here.",
    "kwallet.field.password": "Password",
    "kwallet.field.port": "Port (optional)",
    "kwallet.field.private_key": "SSH private key",
    "kwallet.field.project": "Project ID",
    "kwallet.field.public_key": "Public key",
    "kwallet.field.public_key_hint":
      "The OpenSSH line Techstack installs on the server so this key can log in. A generated key fills it in.",
    "kwallet.field.public_key_optional": "Public key (optional)",
    "kwallet.field.scope": "Scope",
    "kwallet.field.scope_fixed": "This item belongs to this stack.",
    "kwallet.field.secret": "Secret value",
    "kwallet.field.segment_hint":
      "Letters, digits, dots, dashes and underscores, up to 64 characters.",
    "kwallet.field.totp": "One-time code secret (optional)",
    "kwallet.field.type": "Type",
    "kwallet.field.user": "User",
    "kwallet.field.username": "Username",
    "kwallet.field.value": "Value",
    "kwallet.field.value_access": "Who may see the value",
    "kwallet.field.variable_name": "Variable name",
    "kwallet.field.variable_name_hint":
      "What your tools look it up by, such as DATABASE_URL: capital letters, digits and underscores, not starting with a digit. Unique within the scope.",
    "kwallet.filter.all": "All",
    "kwallet.filter.no_match": "No items match your search or filters.",
    "kwallet.filter.scope": "Scope",
    "kwallet.filter.search": "Search",
    "kwallet.filter.search_placeholder": "Name, description or stack",
    "kwallet.filter.type": "Type",
    "kwallet.import.decrypt": "Decrypt file",
    "kwallet.import.description":
      "Add credentials from an earlier export to your Wallet. Each one is encrypted in this browser.",
    "kwallet.import.error.password":
      "The password is wrong or the file is damaged.",
    "kwallet.import.error.unreadable":
      "This file could not be read. Use a Techstack export (JSON or CSV) or a Bitwarden JSON export.",
    "kwallet.import.file": "Export file",
    "kwallet.import.formats":
      "Supported: Techstack JSON (also password-protected), Techstack CSV and Bitwarden JSON.",
    "kwallet.import.no_export":
      "Your Wallet cannot be exported as plain text. To take a value out, reveal it item by item.",
    "kwallet.import.open": "Import",
    "kwallet.import.password": "Export password",
    "kwallet.import.preview": "{count} of {total} entries can be imported.",
    "kwallet.import.result": "{created} imported, {failed} failed.",
    "kwallet.import.row.created": "Imported",
    "kwallet.import.row.name_invalid":
      "Skipped: the name is empty or not allowed.",
    "kwallet.import.row.no_secret": "Skipped: no secret value.",
    "kwallet.import.row.too_large": "Skipped: the value is larger than 64 KiB.",
    "kwallet.import.run": "Import {count} entries",
    "kwallet.import.title": "Import credentials",
    "kwallet.keygen.copied": "Copied",
    "kwallet.keygen.copy": "Copy public key",
    "kwallet.keygen.generate": "Generate a new key pair",
    "kwallet.keygen.generating": "Generating…",
    "kwallet.keygen.hint":
      "An Ed25519 key pair is made in this browser. The private key is saved to your Wallet and never sent anywhere else.",
    "kwallet.keygen.install_hint":
      "Install this public key on the server (in authorized_keys), or let Techstack install it later.",
    "kwallet.keygen.open": "Generate SSH key",
    "kwallet.keygen.public_key": "Public key",
    "kwallet.lead":
      "API keys, logins, variables, configurations and server access in your kombify Wallet. Values are sealed in this browser for the Wallet and kept encrypted there. You decide who may see or use each one.",
    "kwallet.list_caption": "Items in your Wallet",
    "kwallet.load_error_title": "Your Wallet could not be loaded",
    "kwallet.loading": "Loading your Wallet…",
    "kwallet.manage_in_cloud": "Manage in kombify Cloud",
    "kwallet.notice.audit_pending":
      "The change is saved. Its audit record is still being written and follows shortly.",
    "kwallet.notice.configured": "The settings of {name} were saved.",
    "kwallet.notice.created": "{name} was added to your Wallet.",
    "kwallet.notice.deleted": "{name} was deleted.",
    "kwallet.notice.revoked": "{name} was revoked.",
    "kwallet.notice.versioned": "{name} has a new version.",
    "kwallet.picker.create": "Create server access",
    "kwallet.picker.empty": "You have no server access in your Wallet yet.",
    "kwallet.picker.hidden":
      "{count} more are hidden because they are for another server or sign-in method.",
    "kwallet.picker.host_first":
      "Enter the server's host first; it is part of the access.",
    "kwallet.picker.host_unknown": "(host not recorded)",
    "kwallet.picker.label": "Server access from your Wallet",
    "kwallet.picker.none_fit":
      "No server access in your Wallet fits this server and sign-in method.",
    "kwallet.picker.open_wallet": "Open your Wallet",
    "kwallet.picker.placeholder": "Choose server access",
    "kwallet.picker.rebind_label": "Use different server access (optional)",
    "kwallet.picker.techstack_notice":
      "Techstack may now use this item to reach your server. You can withdraw that in your Wallet.",
    "kwallet.prompt.create.allow": "Allow Techstack to save credentials",
    "kwallet.prompt.create.body":
      "To set this up, Techstack has to save credentials in your kombify Wallet. Allow it once, then start again. You can withdraw the permission in your Wallet at any time.",
    "kwallet.prompt.create.done":
      "Techstack may now save credentials in your Wallet. Start again.",
    "kwallet.prompt.create.title": "Techstack needs your permission",
    "kwallet.prompt.later": "Not now",
    "kwallet.prompt.use.allow": "Allow Techstack to use this item",
    "kwallet.prompt.use.body":
      "Techstack has to use this Wallet item to continue. Allow it, then start again. You can withdraw the permission in your Wallet at any time.",
    "kwallet.prompt.use.done": "Techstack may now use this item. Start again.",
    "kwallet.prompt.use.title": "Techstack needs access to this item",
    "kwallet.retry": "Try again",
    "kwallet.reveal.again": "Reveal again",
    "kwallet.reveal.cleared": "The value was hidden again.",
    "kwallet.reveal.copied": "Copied",
    "kwallet.reveal.copy": "Copy",
    "kwallet.reveal.copy_failed":
      "Copying failed. Show the value and copy it by hand.",
    "kwallet.reveal.copy_named": "Copy {field}",
    "kwallet.reveal.countdown": "Hidden again in {seconds} s",
    "kwallet.reveal.description":
      "Shown only in this browser and hidden again after 30 seconds.",
    "kwallet.reveal.hidden": "Hidden",
    "kwallet.reveal.hide": "Hide",
    "kwallet.reveal.loading": "Decrypting in this browser…",
    "kwallet.reveal.show": "Show",
    "kwallet.reveal.title": "Reveal {name}",
    "kwallet.reveal.version": "Version {version}",
    "kwallet.revoke.body":
      "A revoked item can no longer be revealed or used. This cannot be undone; add a new item to replace it.",
    "kwallet.revoke.confirm": "Revoke item",
    "kwallet.revoke.title": "Revoke {name}?",
    "kwallet.rotate.hint":
      "Rotate recommended: the value was released {count} times since its last version. Add a new version to replace it.",
    "kwallet.save": "Save to Wallet",
    "kwallet.saving": "Encrypting and saving…",
    "kwallet.scope.account": "Account",
    "kwallet.scope.environment": "Project {project}, environment {environment}",
    "kwallet.scope.project": "Project {project}",
    "kwallet.scope.stack": "Stack {stack}",
    "kwallet.scope_option.account": "Whole account",
    "kwallet.scope_option.environment": "One environment of a project",
    "kwallet.scope_option.project": "One project",
    "kwallet.settings.description":
      "Who may see the value and where agents may use it.",
    "kwallet.settings.save": "Save settings",
    "kwallet.settings.title": "Settings of {name}",
    "kwallet.sign.again": "Sign another",
    "kwallet.sign.certificate": "Certificate",
    "kwallet.sign.copy": "Copy certificate",
    "kwallet.sign.description":
      "Paste the public key you log in with. The Wallet signs it for the principals and the time you choose, inside this authority's sign policy. Your private key stays on your computer.",
    "kwallet.sign.download": "Download",
    "kwallet.sign.download_failed":
      "The download could not start. Copy the certificate instead.",
    "kwallet.sign.duration": "Valid for",
    "kwallet.sign.duration_hint": "At most {max}. For example 30m or 8h.",
    "kwallet.sign.error.key_invalid":
      "Paste one OpenSSH public key line that starts with ssh-ed25519, ecdsa-sha2-nistp256 or ssh-rsa.",
    "kwallet.sign.error.key_required": "Paste your public key.",
    "kwallet.sign.error.principal_invalid":
      "“{principal}” is not a valid name. User names use letters, digits, dots, dashes and underscores; host names use lowercase letters, digits, dots and dashes.",
    "kwallet.sign.error.principal_outside":
      "“{principal}” is not allowed by this authority's sign policy.",
    "kwallet.sign.error.principals_too_many":
      "A certificate can name at most {max} principals.",
    "kwallet.sign.error.private_key":
      "That looks like a private key. It was cleared and not sent. Paste the public key (the .pub file) instead.",
    "kwallet.sign.error.source_invalid":
      "Enter IP addresses or networks separated by commas, such as 203.0.113.7 or 198.51.100.0/24.",
    "kwallet.sign.error.source_required":
      "Enter the source address the certificate works from.",
    "kwallet.sign.error.ttl_invalid":
      "Enter a duration of at least 1m and at most {max}, such as 30m or 8h.",
    "kwallet.sign.install_hint":
      "Save it as {file} next to your private key, for example in ~/.ssh, then log in as usual.",
    "kwallet.sign.kind": "Certificate type",
    "kwallet.sign.kind_host":
      "Host certificate (for a server to identify itself)",
    "kwallet.sign.kind_user": "User certificate (to log in to a server)",
    "kwallet.sign.principals": "Principals",
    "kwallet.sign.principals_hint":
      "The names the certificate is valid for, separated by commas. Allowed here: {allowed}",
    "kwallet.sign.public_key": "Your public key",
    "kwallet.sign.public_key_hint":
      "Paste the contents of your .pub file, such as ~/.ssh/id_ed25519.pub. Ed25519, ECDSA P-256 and RSA keys work. Never paste a private key.",
    "kwallet.sign.result_principals": "Principals:",
    "kwallet.sign.source": "Source address",
    "kwallet.sign.source_hint":
      "Optional. The addresses or networks the certificate works from, separated by commas, such as 203.0.113.7 or 198.51.100.0/24.",
    "kwallet.sign.source_hint_required":
      "Required by the sign policy. The addresses or networks the certificate works from, separated by commas, such as 203.0.113.7 or 198.51.100.0/24.",
    "kwallet.sign.submit": "Sign certificate",
    "kwallet.sign.title": "Sign a certificate with {name}",
    "kwallet.sign.valid": "Valid from {from} until {until}.",
    "kwallet.ssh_ca.copied": "Copied",
    "kwallet.ssh_ca.copy": "Copy",
    "kwallet.ssh_ca.copy_failed":
      "Copying failed. Select the text and copy it by hand.",
    "kwallet.ssh_ca.copy_key_named": "Copy the public key of {name}",
    "kwallet.ssh_ca.error.kinds_required":
      "Choose at least one kind of certificate.",
    "kwallet.ssh_ca.error.max_host_invalid":
      "Enter a duration from 1m to 366d, such as 90d.",
    "kwallet.ssh_ca.error.max_user_invalid":
      "Enter a duration from 1m to 1d, such as 30m or 8h.",
    "kwallet.ssh_ca.error.principals_invalid":
      "“{principal}” is not a valid principal. Use letters, digits, dots, dashes and underscores, or *. followed by a domain.",
    "kwallet.ssh_ca.error.principals_required": "Enter at least one principal.",
    "kwallet.ssh_ca.error.principals_too_many":
      "A policy can list at most {max} principals.",
    "kwallet.ssh_ca.import_key": "Private key (OpenSSH, no passphrase)",
    "kwallet.ssh_ca.import_key_hint":
      "Paste an unencrypted Ed25519 key that starts with -----BEGIN OPENSSH PRIVATE KEY-----.",
    "kwallet.ssh_ca.no_policy":
      "No sign policy yet, so this authority signs nothing. Set one in its settings.",
    "kwallet.ssh_ca.policy.description":
      "Every certificate it signs stays inside these limits.",
    "kwallet.ssh_ca.policy.duration_hint":
      "A number and a unit: s, m, h or d. For example 30m, 8h or 90d.",
    "kwallet.ssh_ca.policy.extensions": "Extensions of user certificates",
    "kwallet.ssh_ca.policy.extensions_hint":
      "Only the checked extensions can appear in user certificates. permit-pty allows an interactive terminal.",
    "kwallet.ssh_ca.policy.kind_host": "Host certificates",
    "kwallet.ssh_ca.policy.kind_host_hint":
      "For servers to prove who they are. Agents never receive these.",
    "kwallet.ssh_ca.policy.kind_user": "User certificates",
    "kwallet.ssh_ca.policy.kind_user_hint":
      "For people and agents who log in to servers.",
    "kwallet.ssh_ca.policy.kinds": "Kinds of certificate",
    "kwallet.ssh_ca.policy.legend": "What this authority may sign",
    "kwallet.ssh_ca.policy.max_host": "Longest host certificate",
    "kwallet.ssh_ca.policy.max_user": "Longest user certificate",
    "kwallet.ssh_ca.policy.principals": "Allowed principals",
    "kwallet.ssh_ca.policy.principals_hint":
      "One per line or separated by commas. A principal is a user name or a server name. *.example.com stands for exactly one name in front of example.com, such as web1.example.com.",
    "kwallet.ssh_ca.policy.require_source": "Require a source address",
    "kwallet.ssh_ca.policy.require_source_hint":
      "Every user certificate must then name the addresses it works from.",
    "kwallet.ssh_ca.public_key": "Public key",
    "kwallet.ssh_ca.public_key_hint":
      "Servers trust this public key to accept the certificates this authority signs; kombify StackKits installs it when it sets up a server.",
    "kwallet.ssh_ca.settings_description":
      "What this authority may sign. A change applies from the next certificate.",
    "kwallet.ssh_ca.source.generate": "Create it in the Wallet (recommended)",
    "kwallet.ssh_ca.source.generate_hint":
      "The Wallet makes a new Ed25519 key and keeps it. The key never reaches this browser or kombify Cloud.",
    "kwallet.ssh_ca.source.import": "Import an existing key",
    "kwallet.ssh_ca.source.import_hint":
      "For an Ed25519 authority you already use. The key is encrypted in this browser before it is sent.",
    "kwallet.ssh_ca.source_label": "Authority key",
    "kwallet.stack.description":
      "Credentials for this stack are kept in your kombify Wallet. Techstack can use the ones you allow.",
    "kwallet.stack.title": "Credentials in your Wallet",
    "kwallet.state.active": "Active",
    "kwallet.state.deleted": "Deleted",
    "kwallet.state.revoked": "Revoked",
    "kwallet.techstack.allow_hint":
      "Techstack can then use this item for you, for example to reach a server, without you being there. It never gets your other items.",
    "kwallet.techstack.allow_label": "Techstack may use this item",
    "kwallet.techstack.grant_failed":
      'The item was saved, but Techstack could not be allowed to use it. Turn on "Techstack may use this item" in its details.',
    "kwallet.techstack.toggle_named": "Techstack may use {name}",
    "kwallet.title": "Wallet",
    "kwallet.type.ai_credential": "AI provider key",
    "kwallet.type.configuration": "Configuration",
    "kwallet.type.generic_secret": "API key, token or certificate",
    "kwallet.type.login": "Login",
    "kwallet.type.node_access_profile": "Server access",
    "kwallet.type.other": "Other item",
    "kwallet.type.parent_credential": "Parent credential",
    "kwallet.type.ssh_ca": "SSH certificate authority",
    "kwallet.type.variable": "Variable",
    "kwallet.type_hint.configuration":
      "A document, such as an .env file or a configuration file.",
    "kwallet.type_hint.generic_secret":
      "One secret value, such as an API key, a token or a certificate.",
    "kwallet.type_hint.login":
      "A username and password, with an optional one-time code secret.",
    "kwallet.type_hint.node_access_profile":
      "How to reach a server: host, user, and an SSH private key or a password.",
    "kwallet.type_hint.ssh_ca":
      "Signs short-lived SSH certificates for you, your agents and your servers. Its key never leaves the Wallet.",
    "kwallet.type_hint.variable":
      "One value, such as a setting or an identifier.",
    "kwallet.value_access.read_right_holders":
      "Agents and people with read access may see the value",
    "kwallet.value_access.read_right_holders_hint":
      "You can reveal it at any time. An agent sees it only with read access.",
    "kwallet.value_access.use_only":
      "Nobody sees the value, it can only be used",
    "kwallet.value_access.use_only_hint":
      "This hides it from you as well: not even you can reveal it. Agents with use access can only act with it, for example in an API call.",
    "kwallet.version.description":
      "The new value becomes the current version. It is encrypted in this browser before it leaves it.",
    "kwallet.version.save": "Save new version",
    "kwallet.version.title": "New version of {name}",
    "kwallet.wizard.hint":
      "The SSH credential is kept in your kombify Wallet, not in Techstack. Techstack is allowed to use the item you choose.",
    "kwallet.wizard.item_required":
      "Choose or create the server access in your Wallet.",
    "kwallet.working": "Working…",
  },
  de: {
    "kwallet.denied.step_up_required":
      "Ein frischer zweiter Faktor ist nötig. Bestätige ihn und versuche es erneut.",
    "kwallet.step_up.body":
      "Zum Anzeigen eines Werts oder Signieren eines Zertifikats brauchst du deinen zweiten Faktor. Bestätige ihn, um fortzufahren.",
    "kwallet.step_up.body_minutes":
      "Zum Anzeigen eines Werts oder Signieren eines Zertifikats brauchst du deinen zweiten Faktor aus den letzten {minutes} Minuten. Bestätige ihn, um fortzufahren.",
    "kwallet.step_up.confirm": "Mit zweitem Faktor bestätigen",
    "kwallet.step_up.unavailable":
      "In diesem Fenster lässt sich kein zweiter Faktor bestätigen. Öffne Techstack in einem eigenen Browser-Tab und versuche es erneut.",
    "kwallet.step_up.working": "Anmeldung wird geöffnet…",
    "kwallet.action.delete": "Löschen",
    "kwallet.action.delete_named": "{name} löschen",
    "kwallet.action.new_version": "Neue Version",
    "kwallet.action.new_version_named": "Neue Version von {name}",
    "kwallet.action.reveal": "Anzeigen",
    "kwallet.action.reveal_named": "{name} anzeigen",
    "kwallet.action.revoke": "Widerrufen",
    "kwallet.action.revoke_named": "{name} widerrufen",
    "kwallet.action.settings": "Einstellungen",
    "kwallet.action.settings_named": "Einstellungen von {name}",
    "kwallet.action.sign": "Zertifikat signieren",
    "kwallet.action.sign_named": "Zertifikat mit {name} signieren",
    "kwallet.add": "Eintrag hinzufügen",
    "kwallet.api.wallet_create_right_missing":
      "Techstack braucht deine Erlaubnis, Zugangsdaten in deiner kombify Wallet zu speichern. Erlaube es und versuche es dann erneut.",
    "kwallet.api.wallet_item_credential_mismatch":
      "Dieser Wallet-Eintrag enthält nicht die Zugangsdaten für die gewählte Anmeldemethode (SSH-Schlüssel oder Passwort). Wähle einen anderen Eintrag oder ändere die Methode.",
    "kwallet.api.wallet_item_host_mismatch":
      "Der Host in diesem Wallet-Eintrag ist nicht der Host dieses Servers. Wähle einen Eintrag für diesen Host oder lege einen neuen an.",
    "kwallet.api.wallet_item_invalid":
      "Das ist kein gültiger Wallet-Eintrag. Wähle einen aus der Liste.",
    "kwallet.api.wallet_item_no_public_key":
      "Dieser Serverzugang enthält keinen öffentlichen Schlüssel zum Einrichten. Füge dem Eintrag die öffentliche Zeile des Schlüssels hinzu oder nutze ein in deiner Wallet erzeugtes Schlüsselpaar.",
    "kwallet.api.wallet_item_not_found":
      "Dieser Eintrag ist nicht mehr in deiner Wallet. Wähle einen anderen.",
    "kwallet.api.wallet_item_not_node_access":
      "Dieser Eintrag ist kein Serverzugang. Wähle einen Serverzugang.",
    "kwallet.api.wallet_item_required":
      "Wähle einen Serverzugang aus deiner kombify Wallet. Techstack nimmt hier keine Passwörter oder Schlüsselbezeichnungen an.",
    "kwallet.api.wallet_item_unsupported":
      "Diese Installation nutzt die kombify Wallet nicht, daher kann hier kein Wallet-Eintrag verwendet werden.",
    "kwallet.api.wallet_item_unusable":
      "Dieser Wallet-Eintrag wurde widerrufen oder gelöscht und kann nicht genutzt werden. Wähle einen anderen.",
    "kwallet.api.wallet_owner_ineligible":
      "Dein Konto kann die kombify Wallet noch nicht mit Techstack nutzen.",
    "kwallet.api.wallet_response_invalid":
      "Die kombify Wallet hat so geantwortet, dass Techstack die Antwort nicht verwenden kann. Versuche es erneut und melde es, falls es bestehen bleibt.",
    "kwallet.api.wallet_right_missing":
      "Techstack darf diesen Wallet-Eintrag noch nicht nutzen. Erlaube es in deiner Wallet und versuche es dann erneut.",
    "kwallet.api.wallet_right_missing_use":
      "Techstack darf diesen Wallet-Eintrag noch nicht nutzen. Erlaube es und versuche es dann erneut.",
    "kwallet.api.wallet_self_hosted_only":
      "Diese Aktion ist nur bei einem selbst gehosteten Techstack verfügbar.",
    "kwallet.api.wallet_unavailable":
      "Die kombify Wallet ist vorübergehend nicht erreichbar. Versuche es in Kürze erneut.",
    "kwallet.auth.password": "Passwort",
    "kwallet.auth.private_key": "Privater SSH-Schlüssel",
    "kwallet.badge.use_only": "Nur nutzbar",
    "kwallet.cancel": "Abbrechen",
    "kwallet.close": "Schließen",
    "kwallet.view.label": "Ansicht",
    "kwallet.view.mark": "Symbole",
    "kwallet.view.tile": "Kacheln",
    "kwallet.view.row": "Liste",
    "kwallet.view.card": "Karten",
    "kwallet.detail.facts": "Details",
    "kwallet.detail.item": "Eintrag",
    "kwallet.detail.use": "Nutzung",
    "kwallet.detail.history": "Verlauf",
    "kwallet.detail.item_missing":
      "Dieser Eintrag ist nicht in deiner Wallet. Er wurde vielleicht gelöscht oder gehört zu einem anderen Konto.",
    "kwallet.fact.version": "Version",
    "kwallet.fact.use_targets": "Einsatzziele",
    "kwallet.fact.none": "Keine",
    "kwallet.fact.created": "Erstellt",
    "kwallet.fact.last_release": "Zuletzt herausgegeben",
    "kwallet.fact.releases": "Herausgaben seit dieser Version",
    "kwallet.tag.rotate": "Erneuern",
    "kwallet.tag.no_policy": "Keine Signierregel",
    "kwallet.preset.access": "Zugang: Logins und Serverzugänge",
    "kwallet.preset.access_hint":
      "Logins, Serverzugänge und SSH-Zertifizierungsstellen: alles, womit Menschen und Techstack auf die Server und Apps deiner Stacks kommen.",
    "kwallet.preset.recovery":
      "Wiederherstellung: Geheimnisse und Konfigurationen",
    "kwallet.preset.recovery_hint":
      "Geheimnisse und Konfigurationen: der Ort für Wiederherstellungsmaterial und Notfallzugänge.",
    "kwallet.col.scope": "Geltungsbereich",
    "kwallet.col.state": "Status",
    "kwallet.col.techstack": "Techstack",
    "kwallet.col.type": "Typ",
    "kwallet.col.updated": "Aktualisiert",
    "kwallet.create.description":
      "Der Wert wird in diesem Browser versiegelt und verschlüsselt in der Wallet gespeichert. Techstack behält ihn nicht; nur die Zugriffe, die du erteilst, können ihn anzeigen oder nutzen.",
    "kwallet.create.title": "Zur Wallet hinzufügen",
    "kwallet.creator.hint":
      "Techstack legt dann die Zugangsdaten ab, die es für deine Server erzeugt, etwa einen generierten Zugangsschlüssel. Es nutzt nur die Einträge, die du ihm zuweist.",
    "kwallet.creator.label":
      "Techstack erlauben, Zugangsdaten in deiner Wallet zu speichern",
    "kwallet.delete.body":
      "Der Wert und alle seine Versionen werden gelöscht. Das lässt sich nicht rückgängig machen.",
    "kwallet.delete.confirm": "Endgültig löschen",
    "kwallet.delete.title": "{name} löschen?",
    "kwallet.denied.auth_required":
      "Melde dich erneut an, um deine Wallet zu öffnen.",
    "kwallet.denied.capture_invalid":
      "Die Wallet hat den verschlüsselten Wert abgelehnt. Versuche es erneut.",
    "kwallet.denied.capture_key_untrusted":
      "Der Verschlüsselungsschlüssel der Wallet konnte nicht geprüft werden. Es wurde nichts gesendet.",
    "kwallet.denied.conflict":
      "Der Eintrag hat sich inzwischen geändert. Lade die Liste neu und versuche es erneut.",
    "kwallet.denied.generic": "Die Wallet hat die Anfrage abgelehnt.",
    "kwallet.denied.item_action_unsupported":
      "Dieser Eintrag unterstützt diese Aktion nicht. Eine SSH-Zertifizierungsstelle wird nie angezeigt oder erhält nie eine neue Version, und nur eine SSH-Zertifizierungsstelle signiert.",
    "kwallet.denied.item_denied":
      "Dieser Eintrag wurde widerrufen oder gelöscht.",
    "kwallet.denied.item_not_active":
      "Dieser Eintrag wurde widerrufen oder gelöscht. Du kannst Produkten den Zugriff entziehen, aber nicht erteilen.",
    "kwallet.denied.item_not_owned":
      "Dieser Eintrag liegt nicht in deiner Wallet, daher lässt sich der Produktzugriff nicht ändern. Lade neu und versuche es noch einmal.",
    "kwallet.denied.item_unknown":
      "Dieser Eintrag ist nicht mehr in deiner Wallet.",
    "kwallet.denied.name_conflict":
      "In diesem Geltungsbereich gibt es bereits eine Variable oder Konfiguration mit diesem Namen.",
    "kwallet.denied.owner_required":
      "Nur du selbst, persönlich angemeldet, kannst deine Wallet verwalten. Agenten und delegierte Sitzungen können das nicht.",
    "kwallet.denied.owner_unknown":
      "Deine Anmeldung nennt keinen Wallet-Inhaber. Melde dich ab und erneut an.",
    "kwallet.denied.replayed":
      "Diese Änderung wurde bereits gesendet. Versuche es erneut.",
    "kwallet.denied.reveal_mismatch":
      "Der angezeigte Wert passte nicht zu dieser Anfrage und wurde verworfen.",
    "kwallet.denied.shredded": "Diese Wallet wurde gelöscht.",
    "kwallet.denied.sign_mismatch":
      "Die Antwort der Wallet passte nicht zu deiner Anfrage und wurde verworfen.",
    "kwallet.denied.sign_outside_policy":
      "Das Zertifikat liegt außerhalb der Signierrichtlinie dieser Zertifizierungsstelle. Prüfe die Principals, die Gültigkeitsdauer und die Quelladresse.",
    "kwallet.denied.sign_policy_invalid":
      "Die Wallet hat diese Signierrichtlinie abgelehnt. Prüfe die Principals und die Gültigkeitsdauern und versuche es noch einmal.",
    "kwallet.denied.sign_policy_missing":
      "Diese Zertifizierungsstelle hat noch keine Signierrichtlinie und signiert daher nichts. Lege in ihren Einstellungen eine fest.",
    "kwallet.denied.ssh_ca_invalid":
      "Der Schlüssel der Zertifizierungsstelle ist kein verwendbarer unverschlüsselter Ed25519-OpenSSH-Schlüssel. Importiere einen gültigen Schlüssel oder lass die Wallet einen erstellen.",
    "kwallet.denied.subject_key_invalid":
      "Die Wallet kann diesen öffentlichen Schlüssel nicht signieren. Verwende einen Ed25519-, ECDSA-P-256- oder RSA-Schlüssel mit mindestens 2048 Bit.",
    "kwallet.denied.too_large": "Der Wert ist zu groß für die Wallet.",
    "kwallet.denied.transport":
      "Dieser Browser konnte den Wert nicht verschlüsseln oder öffnen. Lade die Seite neu und versuche es erneut.",
    "kwallet.denied.unavailable":
      "Die Wallet ist vorübergehend nicht erreichbar. Versuche es gleich noch einmal.",
    "kwallet.denied.value_use_only":
      "Der Wert dieses Eintrags ist nur nutzbar und kann nicht angezeigt werden.",
    "kwallet.empty_body":
      "Füge einen API-Schlüssel, einen Login, eine Variable, eine Konfiguration oder einen Serverzugang hinzu, um alles an einem Ort zu haben.",
    "kwallet.empty_body_stack":
      "Für diesen Stack sind noch keine Zugangsdaten gespeichert. Füge welche hinzu, damit sie in deiner Wallet liegen und Techstack sie für diesen Stack nutzen kann.",
    "kwallet.empty_title": "Deine Wallet ist leer",
    "kwallet.error.ca_key_encrypted":
      "Dieser Schlüssel ist durch eine Passphrase geschützt. Entferne zuerst die Passphrase oder lass die Wallet eine neue Zertifizierungsstelle erstellen.",
    "kwallet.error.ca_key_invalid":
      "Das ist kein privater OpenSSH-Ed25519-Schlüssel. Füge den ganzen Schlüssel einschließlich der BEGIN- und END-Zeilen ein.",
    "kwallet.error.configuration_name_invalid":
      "Verwende bis zu 128 Kleinbuchstaben, Ziffern, Punkte, Bindestriche oder Unterstriche, mit einem Buchstaben oder einer Ziffer beginnend.",
    "kwallet.error.description_invalid":
      "Verwende bis zu 500 Zeichen, ohne Leerzeichen am Anfang oder Ende.",
    "kwallet.error.host_invalid": "Gib einen Host ohne Leerzeichen ein.",
    "kwallet.error.keygen_unavailable":
      "Dieser Browser kann keinen Ed25519-Schlüssel erzeugen. Füge stattdessen einen vorhandenen Schlüssel ein.",
    "kwallet.error.name_invalid":
      "Verwende 1 bis 120 Zeichen, ohne Leerzeichen am Anfang oder Ende.",
    "kwallet.error.port_invalid":
      "Gib einen Port von 1 bis 65535 ein oder lass das Feld für 22 leer.",
    "kwallet.error.public_key_invalid":
      "Gib eine OpenSSH-Zeile mit einem öffentlichen Schlüssel ein, die mit ssh-ed25519, ecdsa-sha2- oder ssh-rsa beginnt.",
    "kwallet.error.required": "Fülle dieses Feld aus.",
    "kwallet.error.segment_invalid":
      "Verwende Buchstaben, Ziffern, Punkte, Bindestriche oder Unterstriche, bis zu 64 Zeichen.",
    "kwallet.error.value_too_large": "Der Wert ist größer als 64 KiB.",
    "kwallet.error.variable_name_invalid":
      "Verwende bis zu 128 Großbuchstaben, Ziffern oder Unterstriche, nicht mit einer Ziffer beginnend.",
    "kwallet.field.auth": "Anmeldeverfahren",
    "kwallet.field.configuration_name": "Konfigurationsname",
    "kwallet.field.configuration_name_hint":
      "Darunter finden deine Werkzeuge sie, etwa app.env: Kleinbuchstaben, Ziffern, Punkte, Bindestriche und Unterstriche, mit einem Buchstaben oder einer Ziffer beginnend. Im Geltungsbereich eindeutig.",
    "kwallet.field.description": "Beschreibung (optional)",
    "kwallet.field.document": "Dokument",
    "kwallet.field.environment": "Umgebung",
    "kwallet.field.host": "Host",
    "kwallet.field.host_hint":
      "Ein Hostname oder eine IP-Adresse, mit Port, falls er nicht 22 ist.",
    "kwallet.field.name": "Name",
    "kwallet.field.name_hint":
      "Wird in Listen angezeigt. Trage hier nie das Geheimnis selbst ein.",
    "kwallet.field.password": "Passwort",
    "kwallet.field.port": "Port (optional)",
    "kwallet.field.private_key": "Privater SSH-Schlüssel",
    "kwallet.field.project": "Projekt-ID",
    "kwallet.field.public_key": "Öffentlicher Schlüssel",
    "kwallet.field.public_key_hint":
      "Die OpenSSH-Zeile, die Techstack auf dem Server einrichtet, damit sich dieser Schlüssel anmelden kann. Ein erzeugter Schlüssel füllt sie aus.",
    "kwallet.field.public_key_optional": "Öffentlicher Schlüssel (optional)",
    "kwallet.field.scope": "Geltungsbereich",
    "kwallet.field.scope_fixed": "Dieser Eintrag gehört zu diesem Stack.",
    "kwallet.field.secret": "Geheimer Wert",
    "kwallet.field.segment_hint":
      "Buchstaben, Ziffern, Punkte, Bindestriche und Unterstriche, bis zu 64 Zeichen.",
    "kwallet.field.totp": "Geheimnis für Einmalcodes (optional)",
    "kwallet.field.type": "Typ",
    "kwallet.field.user": "Benutzer",
    "kwallet.field.username": "Benutzername",
    "kwallet.field.value": "Wert",
    "kwallet.field.value_access": "Wer den Wert sehen darf",
    "kwallet.field.variable_name": "Variablenname",
    "kwallet.field.variable_name_hint":
      "Darunter finden deine Werkzeuge sie, etwa DATABASE_URL: Großbuchstaben, Ziffern und Unterstriche, nicht mit einer Ziffer beginnend. Im Geltungsbereich eindeutig.",
    "kwallet.filter.all": "Alle",
    "kwallet.filter.no_match":
      "Keine Einträge passen zu deiner Suche oder den Filtern.",
    "kwallet.filter.scope": "Geltungsbereich",
    "kwallet.filter.search": "Suche",
    "kwallet.filter.search_placeholder": "Name, Beschreibung oder Stack",
    "kwallet.filter.type": "Typ",
    "kwallet.import.decrypt": "Datei entschlüsseln",
    "kwallet.import.description":
      "Füge Zugangsdaten aus einem früheren Export zu deiner Wallet hinzu. Jeder Eintrag wird in diesem Browser verschlüsselt.",
    "kwallet.import.error.password":
      "Das Passwort ist falsch oder die Datei ist beschädigt.",
    "kwallet.import.error.unreadable":
      "Diese Datei konnte nicht gelesen werden. Verwende einen Techstack-Export (JSON oder CSV) oder einen Bitwarden-JSON-Export.",
    "kwallet.import.file": "Exportdatei",
    "kwallet.import.formats":
      "Unterstützt: Techstack-JSON (auch passwortgeschützt), Techstack-CSV und Bitwarden-JSON.",
    "kwallet.import.no_export":
      "Deine Wallet lässt sich nicht als Klartext exportieren. Um einen Wert herauszunehmen, zeige ihn Eintrag für Eintrag an.",
    "kwallet.import.open": "Importieren",
    "kwallet.import.password": "Export-Passwort",
    "kwallet.import.preview":
      "{count} von {total} Einträgen können importiert werden.",
    "kwallet.import.result": "{created} importiert, {failed} fehlgeschlagen.",
    "kwallet.import.row.created": "Importiert",
    "kwallet.import.row.name_invalid":
      "Übersprungen: Der Name ist leer oder nicht zulässig.",
    "kwallet.import.row.no_secret": "Übersprungen: kein geheimer Wert.",
    "kwallet.import.row.too_large":
      "Übersprungen: Der Wert ist größer als 64 KiB.",
    "kwallet.import.run": "{count} Einträge importieren",
    "kwallet.import.title": "Zugangsdaten importieren",
    "kwallet.keygen.copied": "Kopiert",
    "kwallet.keygen.copy": "Öffentlichen Schlüssel kopieren",
    "kwallet.keygen.generate": "Neues Schlüsselpaar erzeugen",
    "kwallet.keygen.generating": "Wird erzeugt …",
    "kwallet.keygen.hint":
      "Ein Ed25519-Schlüsselpaar wird in diesem Browser erzeugt. Der private Schlüssel wird in deiner Wallet gespeichert und nirgendwohin sonst gesendet.",
    "kwallet.keygen.install_hint":
      "Richte diesen öffentlichen Schlüssel auf dem Server ein (in authorized_keys) oder lass ihn später von Techstack einrichten.",
    "kwallet.keygen.open": "SSH-Schlüssel erzeugen",
    "kwallet.keygen.public_key": "Öffentlicher Schlüssel",
    "kwallet.lead":
      "API-Schlüssel, Logins, Variablen, Konfigurationen und Serverzugänge in deiner kombify Wallet. Werte werden in diesem Browser für die Wallet versiegelt und dort verschlüsselt aufbewahrt. Du entscheidest, wer sie sehen oder nutzen darf.",
    "kwallet.list_caption": "Einträge in deiner Wallet",
    "kwallet.load_error_title": "Deine Wallet konnte nicht geladen werden",
    "kwallet.loading": "Deine Wallet wird geladen …",
    "kwallet.manage_in_cloud": "In kombify Cloud verwalten",
    "kwallet.notice.audit_pending":
      "Die Änderung ist gespeichert. Ihr Audit-Eintrag wird noch geschrieben und folgt in Kürze.",
    "kwallet.notice.configured":
      "Die Einstellungen von {name} wurden gespeichert.",
    "kwallet.notice.created": "{name} wurde deiner Wallet hinzugefügt.",
    "kwallet.notice.deleted": "{name} wurde gelöscht.",
    "kwallet.notice.revoked": "{name} wurde widerrufen.",
    "kwallet.notice.versioned": "{name} hat eine neue Version.",
    "kwallet.picker.create": "Serverzugang anlegen",
    "kwallet.picker.empty":
      "In deiner Wallet gibt es noch keinen Serverzugang.",
    "kwallet.picker.hidden":
      "{count} weitere sind ausgeblendet, weil sie für einen anderen Server oder eine andere Anmeldemethode gelten.",
    "kwallet.picker.host_first":
      "Gib zuerst den Host des Servers ein; er gehört zum Zugang.",
    "kwallet.picker.host_unknown": "(Host nicht vermerkt)",
    "kwallet.picker.label": "Serverzugang aus deiner Wallet",
    "kwallet.picker.none_fit":
      "Kein Serverzugang in deiner Wallet passt zu diesem Server und dieser Anmeldemethode.",
    "kwallet.picker.open_wallet": "Wallet öffnen",
    "kwallet.picker.placeholder": "Serverzugang wählen",
    "kwallet.picker.rebind_label": "Anderen Serverzugang verwenden (optional)",
    "kwallet.picker.techstack_notice":
      "Techstack darf diesen Eintrag jetzt nutzen, um deinen Server zu erreichen. Du kannst das in deiner Wallet zurücknehmen.",
    "kwallet.prompt.create.allow":
      "Techstack das Speichern von Zugangsdaten erlauben",
    "kwallet.prompt.create.body":
      "Dafür muss Techstack Zugangsdaten in deiner kombify Wallet speichern. Erlaube das einmal und starte dann erneut. Du kannst die Erlaubnis jederzeit in deiner Wallet zurücknehmen.",
    "kwallet.prompt.create.done":
      "Techstack darf jetzt Zugangsdaten in deiner Wallet speichern. Starte erneut.",
    "kwallet.prompt.create.title": "Techstack braucht deine Erlaubnis",
    "kwallet.prompt.later": "Jetzt nicht",
    "kwallet.prompt.use.allow":
      "Techstack die Nutzung dieses Eintrags erlauben",
    "kwallet.prompt.use.body":
      "Techstack muss diesen Wallet-Eintrag nutzen, um fortzufahren. Erlaube es und starte dann erneut. Du kannst die Erlaubnis jederzeit in deiner Wallet zurücknehmen.",
    "kwallet.prompt.use.done":
      "Techstack darf diesen Eintrag jetzt nutzen. Starte erneut.",
    "kwallet.prompt.use.title": "Techstack braucht Zugriff auf diesen Eintrag",
    "kwallet.retry": "Erneut versuchen",
    "kwallet.reveal.again": "Erneut anzeigen",
    "kwallet.reveal.cleared": "Der Wert wurde wieder verborgen.",
    "kwallet.reveal.copied": "Kopiert",
    "kwallet.reveal.copy": "Kopieren",
    "kwallet.reveal.copy_failed":
      "Kopieren fehlgeschlagen. Blende den Wert ein und kopiere ihn von Hand.",
    "kwallet.reveal.copy_named": "{field} kopieren",
    "kwallet.reveal.countdown": "Wird in {seconds} s wieder verborgen",
    "kwallet.reveal.description":
      "Wird nur in diesem Browser angezeigt und nach 30 Sekunden wieder verborgen.",
    "kwallet.reveal.hidden": "Verborgen",
    "kwallet.reveal.hide": "Ausblenden",
    "kwallet.reveal.loading": "Wird in diesem Browser entschlüsselt …",
    "kwallet.reveal.show": "Einblenden",
    "kwallet.reveal.title": "{name} anzeigen",
    "kwallet.reveal.version": "Version {version}",
    "kwallet.revoke.body":
      "Ein widerrufener Eintrag kann nicht mehr angezeigt oder verwendet werden. Das lässt sich nicht rückgängig machen; lege einen neuen Eintrag als Ersatz an.",
    "kwallet.revoke.confirm": "Eintrag widerrufen",
    "kwallet.revoke.title": "{name} widerrufen?",
    "kwallet.rotate.hint":
      "Erneuern empfohlen: Der Wert wurde seit seiner letzten Version {count}-mal freigegeben. Lege eine neue Version an, um ihn zu ersetzen.",
    "kwallet.save": "In der Wallet speichern",
    "kwallet.saving": "Wird verschlüsselt und gespeichert …",
    "kwallet.scope.account": "Konto",
    "kwallet.scope.environment": "Projekt {project}, Umgebung {environment}",
    "kwallet.scope.project": "Projekt {project}",
    "kwallet.scope.stack": "Stack {stack}",
    "kwallet.scope_option.account": "Ganzes Konto",
    "kwallet.scope_option.environment": "Eine Umgebung eines Projekts",
    "kwallet.scope_option.project": "Ein Projekt",
    "kwallet.settings.description":
      "Wer den Wert sehen darf und wo Agenten ihn nutzen dürfen.",
    "kwallet.settings.save": "Einstellungen speichern",
    "kwallet.settings.title": "Einstellungen von {name}",
    "kwallet.sign.again": "Weiteres signieren",
    "kwallet.sign.certificate": "Zertifikat",
    "kwallet.sign.copy": "Zertifikat kopieren",
    "kwallet.sign.description":
      "Füge den öffentlichen Schlüssel ein, mit dem du dich anmeldest. Die Wallet signiert ihn für die Principals und die Zeit, die du wählst, innerhalb der Signierrichtlinie dieser Zertifizierungsstelle. Dein privater Schlüssel bleibt auf deinem Computer.",
    "kwallet.sign.download": "Herunterladen",
    "kwallet.sign.download_failed":
      "Der Download konnte nicht starten. Kopiere stattdessen das Zertifikat.",
    "kwallet.sign.duration": "Gültig für",
    "kwallet.sign.duration_hint": "Höchstens {max}. Zum Beispiel 30m oder 8h.",
    "kwallet.sign.error.key_invalid":
      "Füge eine OpenSSH-Zeile mit einem öffentlichen Schlüssel ein, die mit ssh-ed25519, ecdsa-sha2-nistp256 oder ssh-rsa beginnt.",
    "kwallet.sign.error.key_required":
      "Füge deinen öffentlichen Schlüssel ein.",
    "kwallet.sign.error.principal_invalid":
      "„{principal}“ ist kein gültiger Name. Benutzernamen bestehen aus Buchstaben, Ziffern, Punkten, Bindestrichen und Unterstrichen; Hostnamen aus Kleinbuchstaben, Ziffern, Punkten und Bindestrichen.",
    "kwallet.sign.error.principal_outside":
      "„{principal}“ ist durch die Signierrichtlinie dieser Zertifizierungsstelle nicht erlaubt.",
    "kwallet.sign.error.principals_too_many":
      "Ein Zertifikat kann höchstens {max} Principals nennen.",
    "kwallet.sign.error.private_key":
      "Das sieht nach einem privaten Schlüssel aus. Er wurde gelöscht und nicht gesendet. Füge stattdessen den öffentlichen Schlüssel ein (die .pub-Datei).",
    "kwallet.sign.error.source_invalid":
      "Gib IP-Adressen oder Netze durch Kommas getrennt ein, zum Beispiel 203.0.113.7 oder 198.51.100.0/24.",
    "kwallet.sign.error.source_required":
      "Gib die Quelladresse ein, von der aus das Zertifikat gilt.",
    "kwallet.sign.error.ttl_invalid":
      "Gib eine Dauer von mindestens 1m und höchstens {max} ein, zum Beispiel 30m oder 8h.",
    "kwallet.sign.install_hint":
      "Speichere es als {file} neben deinem privaten Schlüssel, zum Beispiel in ~/.ssh, und melde dich dann wie gewohnt an.",
    "kwallet.sign.kind": "Zertifikatart",
    "kwallet.sign.kind_host": "Hostzertifikat (damit sich ein Server ausweist)",
    "kwallet.sign.kind_user":
      "Benutzerzertifikat (zum Anmelden an einem Server)",
    "kwallet.sign.principals": "Principals",
    "kwallet.sign.principals_hint":
      "Die Namen, für die das Zertifikat gilt, durch Kommas getrennt. Hier erlaubt: {allowed}",
    "kwallet.sign.public_key": "Dein öffentlicher Schlüssel",
    "kwallet.sign.public_key_hint":
      "Füge den Inhalt deiner .pub-Datei ein, zum Beispiel ~/.ssh/id_ed25519.pub. Ed25519-, ECDSA-P-256- und RSA-Schlüssel funktionieren. Füge niemals einen privaten Schlüssel ein.",
    "kwallet.sign.result_principals": "Principals:",
    "kwallet.sign.source": "Quelladresse",
    "kwallet.sign.source_hint":
      "Optional. Die Adressen oder Netze, von denen aus das Zertifikat gilt, durch Kommas getrennt, zum Beispiel 203.0.113.7 oder 198.51.100.0/24.",
    "kwallet.sign.source_hint_required":
      "Von der Signierrichtlinie verlangt. Die Adressen oder Netze, von denen aus das Zertifikat gilt, durch Kommas getrennt, zum Beispiel 203.0.113.7 oder 198.51.100.0/24.",
    "kwallet.sign.submit": "Zertifikat signieren",
    "kwallet.sign.title": "Zertifikat mit {name} signieren",
    "kwallet.sign.valid": "Gültig von {from} bis {until}.",
    "kwallet.ssh_ca.copied": "Kopiert",
    "kwallet.ssh_ca.copy": "Kopieren",
    "kwallet.ssh_ca.copy_failed":
      "Das Kopieren ist fehlgeschlagen. Markiere den Text und kopiere ihn von Hand.",
    "kwallet.ssh_ca.copy_key_named":
      "Öffentlichen Schlüssel von {name} kopieren",
    "kwallet.ssh_ca.error.kinds_required":
      "Wähle mindestens eine Zertifikatart.",
    "kwallet.ssh_ca.error.max_host_invalid":
      "Gib eine Dauer von 1m bis 366d ein, zum Beispiel 90d.",
    "kwallet.ssh_ca.error.max_user_invalid":
      "Gib eine Dauer von 1m bis 1d ein, zum Beispiel 30m oder 8h.",
    "kwallet.ssh_ca.error.principals_invalid":
      "„{principal}“ ist kein gültiger Principal. Verwende Buchstaben, Ziffern, Punkte, Bindestriche und Unterstriche oder *. gefolgt von einer Domain.",
    "kwallet.ssh_ca.error.principals_required":
      "Gib mindestens einen Principal ein.",
    "kwallet.ssh_ca.error.principals_too_many":
      "Eine Richtlinie kann höchstens {max} Principals enthalten.",
    "kwallet.ssh_ca.import_key":
      "Privater Schlüssel (OpenSSH, ohne Passphrase)",
    "kwallet.ssh_ca.import_key_hint":
      "Füge einen unverschlüsselten Ed25519-Schlüssel ein, der mit -----BEGIN OPENSSH PRIVATE KEY----- beginnt.",
    "kwallet.ssh_ca.no_policy":
      "Noch keine Signierrichtlinie, daher signiert diese Zertifizierungsstelle nichts. Lege in ihren Einstellungen eine fest.",
    "kwallet.ssh_ca.policy.description":
      "Jedes Zertifikat, das sie signiert, bleibt innerhalb dieser Grenzen.",
    "kwallet.ssh_ca.policy.duration_hint":
      "Eine Zahl und eine Einheit: s, m, h oder d. Zum Beispiel 30m, 8h oder 90d.",
    "kwallet.ssh_ca.policy.extensions":
      "Erweiterungen von Benutzerzertifikaten",
    "kwallet.ssh_ca.policy.extensions_hint":
      "Nur die markierten Erweiterungen können in Benutzerzertifikaten stehen. permit-pty erlaubt ein interaktives Terminal.",
    "kwallet.ssh_ca.policy.kind_host": "Hostzertifikate",
    "kwallet.ssh_ca.policy.kind_host_hint":
      "Damit Server nachweisen, wer sie sind. Agenten erhalten diese nie.",
    "kwallet.ssh_ca.policy.kind_user": "Benutzerzertifikate",
    "kwallet.ssh_ca.policy.kind_user_hint":
      "Für Personen und Agenten, die sich an Servern anmelden.",
    "kwallet.ssh_ca.policy.kinds": "Zertifikatarten",
    "kwallet.ssh_ca.policy.legend":
      "Was diese Zertifizierungsstelle signieren darf",
    "kwallet.ssh_ca.policy.max_host": "Längstes Hostzertifikat",
    "kwallet.ssh_ca.policy.max_user": "Längstes Benutzerzertifikat",
    "kwallet.ssh_ca.policy.principals": "Erlaubte Principals",
    "kwallet.ssh_ca.policy.principals_hint":
      "Einer pro Zeile oder durch Kommas getrennt. Ein Principal ist ein Benutzer- oder Servername. *.example.com steht für genau einen Namen vor example.com, zum Beispiel web1.example.com.",
    "kwallet.ssh_ca.policy.require_source": "Quelladresse verlangen",
    "kwallet.ssh_ca.policy.require_source_hint":
      "Jedes Benutzerzertifikat muss dann die Adressen nennen, von denen aus es gilt.",
    "kwallet.ssh_ca.public_key": "Öffentlicher Schlüssel",
    "kwallet.ssh_ca.public_key_hint":
      "Server vertrauen diesem öffentlichen Schlüssel und akzeptieren so die Zertifikate dieser Zertifizierungsstelle; kombify StackKits installiert ihn, wenn es einen Server einrichtet.",
    "kwallet.ssh_ca.settings_description":
      "Was diese Zertifizierungsstelle signieren darf. Eine Änderung gilt ab dem nächsten Zertifikat.",
    "kwallet.ssh_ca.source.generate": "In der Wallet erstellen (empfohlen)",
    "kwallet.ssh_ca.source.generate_hint":
      "Die Wallet erzeugt einen neuen Ed25519-Schlüssel und bewahrt ihn auf. Der Schlüssel erreicht weder diesen Browser noch kombify Cloud.",
    "kwallet.ssh_ca.source.import": "Vorhandenen Schlüssel importieren",
    "kwallet.ssh_ca.source.import_hint":
      "Für eine Ed25519-Zertifizierungsstelle, die du bereits nutzt. Der Schlüssel wird in diesem Browser verschlüsselt, bevor er gesendet wird.",
    "kwallet.ssh_ca.source_label": "Schlüssel der Zertifizierungsstelle",
    "kwallet.stack.description":
      "Die Zugangsdaten für diesen Stack liegen in deiner kombify Wallet. Techstack kann die nutzen, die du erlaubst.",
    "kwallet.stack.title": "Zugangsdaten in deiner Wallet",
    "kwallet.state.active": "Aktiv",
    "kwallet.state.deleted": "Gelöscht",
    "kwallet.state.revoked": "Widerrufen",
    "kwallet.techstack.allow_hint":
      "Techstack kann diesen Eintrag dann für dich nutzen, etwa um einen Server zu erreichen, ohne dass du dabei bist. Deine anderen Einträge bekommt es nie.",
    "kwallet.techstack.allow_label": "Techstack darf diesen Eintrag nutzen",
    "kwallet.techstack.grant_failed":
      "Der Eintrag wurde gespeichert, aber Techstack konnte die Nutzung nicht erlaubt werden. Aktiviere für den Eintrag „Techstack darf diesen Eintrag nutzen“.",
    "kwallet.techstack.toggle_named": "Techstack darf {name} nutzen",
    "kwallet.title": "Wallet",
    "kwallet.type.ai_credential": "Schlüssel eines KI-Anbieters",
    "kwallet.type.configuration": "Konfiguration",
    "kwallet.type.generic_secret": "API-Schlüssel, Token oder Zertifikat",
    "kwallet.type.login": "Login",
    "kwallet.type.node_access_profile": "Serverzugang",
    "kwallet.type.other": "Anderer Eintrag",
    "kwallet.type.parent_credential": "Übergeordnete Zugangsdaten",
    "kwallet.type.ssh_ca": "SSH-Zertifizierungsstelle",
    "kwallet.type.variable": "Variable",
    "kwallet.type_hint.configuration":
      "Ein Dokument, etwa eine .env-Datei oder eine Konfigurationsdatei.",
    "kwallet.type_hint.generic_secret":
      "Ein geheimer Wert, etwa ein API-Schlüssel, ein Token oder ein Zertifikat.",
    "kwallet.type_hint.login":
      "Benutzername und Passwort, optional mit dem Geheimnis für Einmalcodes.",
    "kwallet.type_hint.node_access_profile":
      "So erreichst du einen Server: Host, Benutzer und ein privater SSH-Schlüssel oder ein Passwort.",
    "kwallet.type_hint.ssh_ca":
      "Signiert kurzlebige SSH-Zertifikate für dich, deine Agenten und deine Server. Ihr Schlüssel verlässt die Wallet nie.",
    "kwallet.type_hint.variable":
      "Ein einzelner Wert, etwa eine Einstellung oder eine Kennung.",
    "kwallet.value_access.read_right_holders":
      "Agenten und Personen mit Lesezugriff dürfen den Wert sehen",
    "kwallet.value_access.read_right_holders_hint":
      "Du kannst ihn jederzeit anzeigen. Ein Agent sieht ihn nur mit Lesezugriff.",
    "kwallet.value_access.use_only":
      "Niemand sieht den Wert, er kann nur genutzt werden",
    "kwallet.value_access.use_only_hint":
      "Das verbirgt ihn auch vor dir: Selbst du kannst ihn nicht mehr anzeigen. Agenten mit Nutzungszugriff können nur damit handeln, zum Beispiel in einem API-Aufruf.",
    "kwallet.version.description":
      "Der neue Wert wird zur aktuellen Version. Er wird in diesem Browser verschlüsselt, bevor er ihn verlässt.",
    "kwallet.version.save": "Neue Version speichern",
    "kwallet.version.title": "Neue Version von {name}",
    "kwallet.wizard.hint":
      "Die SSH-Zugangsdaten liegen in deiner kombify Wallet, nicht in Techstack. Techstack darf den gewählten Eintrag nutzen.",
    "kwallet.wizard.item_required":
      "Wähle den Serverzugang in deiner Wallet oder lege ihn an.",
    "kwallet.working": "Wird ausgeführt …",
  },
  es: {
    "kwallet.denied.step_up_required":
      "Se necesita un segundo factor reciente. Confírmalo e inténtalo de nuevo.",
    "kwallet.step_up.body":
      "Mostrar un valor o firmar un certificado requiere tu segundo factor. Confírmalo para continuar.",
    "kwallet.step_up.body_minutes":
      "Mostrar un valor o firmar un certificado requiere tu segundo factor de los últimos {minutes} minutos. Confírmalo para continuar.",
    "kwallet.step_up.confirm": "Confirmar con el segundo factor",
    "kwallet.step_up.unavailable":
      "No se puede confirmar un segundo factor en esta ventana. Abre Techstack en su propia pestaña e inténtalo de nuevo.",
    "kwallet.step_up.working": "Abriendo el inicio de sesión…",
    "kwallet.action.delete": "Eliminar",
    "kwallet.action.delete_named": "Eliminar {name}",
    "kwallet.action.new_version": "Nueva versión",
    "kwallet.action.new_version_named": "Nueva versión de {name}",
    "kwallet.action.reveal": "Mostrar",
    "kwallet.action.reveal_named": "Mostrar {name}",
    "kwallet.action.revoke": "Revocar",
    "kwallet.action.revoke_named": "Revocar {name}",
    "kwallet.action.settings": "Ajustes",
    "kwallet.action.settings_named": "Ajustes de {name}",
    "kwallet.action.sign": "Firmar certificado",
    "kwallet.action.sign_named": "Firmar un certificado con {name}",
    "kwallet.add": "Añadir elemento",
    "kwallet.api.wallet_create_right_missing":
      "Techstack necesita tu permiso para guardar credenciales en tu kombify Wallet. Permítelo y vuelve a intentarlo.",
    "kwallet.api.wallet_item_credential_mismatch":
      "Este elemento de Wallet no contiene la credencial del método de inicio de sesión elegido (clave SSH o contraseña). Elige otro elemento o cambia el método.",
    "kwallet.api.wallet_item_host_mismatch":
      "El host de este elemento de Wallet no es el host de este servidor. Elige un elemento para este host o crea uno nuevo.",
    "kwallet.api.wallet_item_invalid":
      "Ese no es un elemento de Wallet válido. Elige uno de la lista.",
    "kwallet.api.wallet_item_no_public_key":
      "Este acceso a servidor no contiene ninguna clave pública que instalar. Añade al elemento la línea pública de la clave o usa un par de claves generado en tu Wallet.",
    "kwallet.api.wallet_item_not_found":
      "Este elemento ya no está en tu Wallet. Elige otro.",
    "kwallet.api.wallet_item_not_node_access":
      "Este elemento no es un acceso a servidor. Elige un acceso a servidor.",
    "kwallet.api.wallet_item_required":
      "Elige un acceso a servidor de tu kombify Wallet. Techstack no acepta aquí contraseñas ni etiquetas de clave.",
    "kwallet.api.wallet_item_unsupported":
      "Esta instalación no usa kombify Wallet, por lo que aquí no se puede usar un elemento de Wallet.",
    "kwallet.api.wallet_item_unusable":
      "Este elemento de Wallet se revocó o eliminó y no se puede usar. Elige otro.",
    "kwallet.api.wallet_owner_ineligible":
      "Tu cuenta todavía no puede usar kombify Wallet con Techstack.",
    "kwallet.api.wallet_response_invalid":
      "kombify Wallet respondió de una forma que Techstack no puede usar. Inténtalo de nuevo e infórmalo si persiste.",
    "kwallet.api.wallet_right_missing":
      "Techstack todavía no puede usar este elemento de Wallet. Permítelo en tu Wallet y vuelve a intentarlo.",
    "kwallet.api.wallet_right_missing_use":
      "Techstack todavía no puede usar este elemento de Wallet. Permítelo y vuelve a intentarlo.",
    "kwallet.api.wallet_self_hosted_only":
      "Esta acción solo está disponible en un Techstack autoalojado.",
    "kwallet.api.wallet_unavailable":
      "kombify Wallet no está disponible temporalmente. Inténtalo de nuevo en breve.",
    "kwallet.auth.password": "Contraseña",
    "kwallet.auth.private_key": "Clave privada SSH",
    "kwallet.badge.use_only": "Solo uso",
    "kwallet.cancel": "Cancelar",
    "kwallet.close": "Cerrar",
    "kwallet.view.label": "Vista",
    "kwallet.view.mark": "Iconos",
    "kwallet.view.tile": "Mosaicos",
    "kwallet.view.row": "Lista",
    "kwallet.view.card": "Tarjetas",
    "kwallet.detail.facts": "Detalles",
    "kwallet.detail.item": "Elemento",
    "kwallet.detail.use": "Uso",
    "kwallet.detail.history": "Historial",
    "kwallet.detail.item_missing":
      "Este elemento no está en tu Wallet. Puede que se haya eliminado o que pertenezca a otra cuenta.",
    "kwallet.fact.version": "Versión",
    "kwallet.fact.use_targets": "Destinos de uso",
    "kwallet.fact.none": "Ninguno",
    "kwallet.fact.created": "Creado",
    "kwallet.fact.last_release": "Última entrega",
    "kwallet.fact.releases": "Entregas desde esta versión",
    "kwallet.tag.rotate": "Rotar",
    "kwallet.tag.no_policy": "Sin política de firma",
    "kwallet.preset.access": "Acceso: inicios de sesión y acceso a servidores",
    "kwallet.preset.access_hint":
      "Inicios de sesión, acceso a servidores y autoridades de certificación SSH: lo que permite a las personas y a Techstack entrar en los servidores y apps de tus stacks.",
    "kwallet.preset.recovery": "Recuperación: secretos y configuraciones",
    "kwallet.preset.recovery_hint":
      "Secretos y configuraciones: el lugar para el material de recuperación y las credenciales de emergencia.",
    "kwallet.col.scope": "Ámbito",
    "kwallet.col.state": "Estado",
    "kwallet.col.techstack": "Techstack",
    "kwallet.col.type": "Tipo",
    "kwallet.col.updated": "Actualizado",
    "kwallet.create.description":
      "El valor se cifra en este navegador. Techstack nunca lo ve.",
    "kwallet.create.title": "Añadir a tu Wallet",
    "kwallet.creator.hint":
      "Techstack añadirá entonces las credenciales que crea para tus servidores, como una clave de acceso generada. Solo usa los elementos que le asignes.",
    "kwallet.creator.label":
      "Permitir que Techstack guarde credenciales en tu Wallet",
    "kwallet.delete.body":
      "Se borran el valor y todas sus versiones. No se puede deshacer.",
    "kwallet.delete.confirm": "Eliminar definitivamente",
    "kwallet.delete.title": "¿Eliminar {name}?",
    "kwallet.denied.auth_required":
      "Vuelve a iniciar sesión para acceder a tu Wallet.",
    "kwallet.denied.capture_invalid":
      "Wallet rechazó el valor cifrado. Inténtalo de nuevo.",
    "kwallet.denied.capture_key_untrusted":
      "No se pudo verificar la clave de cifrado de Wallet. No se envió nada.",
    "kwallet.denied.conflict":
      "El elemento cambió mientras tanto. Recarga la lista e inténtalo de nuevo.",
    "kwallet.denied.generic": "Wallet rechazó la solicitud.",
    "kwallet.denied.item_action_unsupported":
      "Este elemento no admite esa acción. Una autoridad SSH nunca se muestra ni recibe una versión nueva, y solo una autoridad SSH firma.",
    "kwallet.denied.item_denied": "Este elemento se revocó o se eliminó.",
    "kwallet.denied.item_not_active":
      "Este elemento se revocó o se eliminó. Puedes retirar el acceso de los productos, pero no concederlo.",
    "kwallet.denied.item_not_owned":
      "Este elemento no está en tu Wallet, así que no se puede cambiar el acceso de los productos. Recarga e inténtalo de nuevo.",
    "kwallet.denied.item_unknown": "Este elemento ya no está en tu Wallet.",
    "kwallet.denied.name_conflict":
      "Ya existe una variable o configuración con este nombre en este ámbito.",
    "kwallet.denied.owner_required":
      "Solo tú, con tu propia sesión, puedes gestionar tu Wallet. Los agentes y las sesiones delegadas no pueden.",
    "kwallet.denied.owner_unknown":
      "Tu inicio de sesión no identifica a un titular de Wallet. Cierra sesión y vuelve a iniciarla.",
    "kwallet.denied.replayed": "Este cambio ya se envió. Inténtalo de nuevo.",
    "kwallet.denied.reveal_mismatch":
      "El valor mostrado no coincidía con esta solicitud y se descartó.",
    "kwallet.denied.shredded": "Esta Wallet se borró.",
    "kwallet.denied.sign_mismatch":
      "La respuesta de Wallet no coincidía con tu solicitud y se descartó.",
    "kwallet.denied.sign_outside_policy":
      "El certificado queda fuera de la política de firma de esta autoridad. Revisa los principals, la duración y la dirección de origen.",
    "kwallet.denied.sign_policy_invalid":
      "Wallet rechazó esta política de firma. Revisa los principals y las duraciones e inténtalo de nuevo.",
    "kwallet.denied.sign_policy_missing":
      "Esta autoridad aún no tiene política de firma, así que no firma nada. Define una en sus ajustes.",
    "kwallet.denied.ssh_ca_invalid":
      "La clave de la autoridad no es una clave OpenSSH Ed25519 sin cifrar utilizable. Importa una clave válida o deja que Wallet cree una.",
    "kwallet.denied.subject_key_invalid":
      "Wallet no puede firmar esta clave pública. Usa una clave Ed25519, ECDSA P-256 o RSA de 2048 bits o más.",
    "kwallet.denied.too_large": "El valor es demasiado grande para Wallet.",
    "kwallet.denied.transport":
      "Este navegador no pudo cifrar ni abrir el valor. Recarga la página e inténtalo de nuevo.",
    "kwallet.denied.unavailable":
      "Wallet no está disponible temporalmente. Inténtalo de nuevo en breve.",
    "kwallet.denied.value_use_only":
      "El valor de este elemento es de solo uso y no se puede mostrar.",
    "kwallet.empty_body":
      "Añade una clave de API, un inicio de sesión, una variable, una configuración o un acceso a servidor para tenerlo todo en un solo lugar.",
    "kwallet.empty_body_stack":
      "Todavía no hay credenciales guardadas para este stack. Añade una para conservarla en tu Wallet, donde Techstack puede usarla para este stack.",
    "kwallet.empty_title": "Tu Wallet está vacía",
    "kwallet.error.ca_key_encrypted":
      "Esta clave está protegida con una frase de contraseña. Quítala primero o deja que Wallet cree una autoridad nueva.",
    "kwallet.error.ca_key_invalid":
      "Esta no es una clave privada Ed25519 de OpenSSH. Pega la clave completa, con las líneas BEGIN y END.",
    "kwallet.error.configuration_name_invalid":
      "Usa hasta 128 letras minúsculas, dígitos, puntos, guiones o guiones bajos, empezando por una letra o un dígito.",
    "kwallet.error.description_invalid":
      "Usa hasta 500 caracteres, sin espacios al principio ni al final.",
    "kwallet.error.host_invalid": "Introduce un host sin espacios.",
    "kwallet.error.keygen_unavailable":
      "Este navegador no puede generar una clave Ed25519. Pega una clave existente.",
    "kwallet.error.name_invalid":
      "Usa de 1 a 120 caracteres, sin espacios al principio ni al final.",
    "kwallet.error.port_invalid":
      "Introduce un puerto de 1 a 65535 o déjalo vacío para usar el 22.",
    "kwallet.error.public_key_invalid":
      "Introduce una línea de clave pública OpenSSH que empiece por ssh-ed25519, ecdsa-sha2- o ssh-rsa.",
    "kwallet.error.required": "Rellena este campo.",
    "kwallet.error.segment_invalid":
      "Usa letras, dígitos, puntos, guiones o guiones bajos, hasta 64 caracteres.",
    "kwallet.error.value_too_large": "El valor supera los 64 KiB.",
    "kwallet.error.variable_name_invalid":
      "Usa hasta 128 letras mayúsculas, dígitos o guiones bajos, sin empezar por un dígito.",
    "kwallet.field.auth": "Método de acceso",
    "kwallet.field.configuration_name": "Nombre de la configuración",
    "kwallet.field.configuration_name_hint":
      "Es el nombre con el que la buscan tus herramientas, como app.env: letras minúsculas, dígitos, puntos, guiones y guiones bajos, empezando por una letra o un dígito. Único dentro del ámbito.",
    "kwallet.field.description": "Descripción (opcional)",
    "kwallet.field.document": "Documento",
    "kwallet.field.environment": "Entorno",
    "kwallet.field.host": "Host",
    "kwallet.field.host_hint":
      "Un nombre de host o una dirección IP, con el puerto si no es el 22.",
    "kwallet.field.name": "Nombre",
    "kwallet.field.name_hint":
      "Se muestra en las listas. Nunca pongas aquí el secreto.",
    "kwallet.field.password": "Contraseña",
    "kwallet.field.port": "Puerto (opcional)",
    "kwallet.field.private_key": "Clave privada SSH",
    "kwallet.field.project": "ID del proyecto",
    "kwallet.field.public_key": "Clave pública",
    "kwallet.field.public_key_hint":
      "La línea OpenSSH que Techstack instala en el servidor para que esta clave pueda iniciar sesión. Una clave generada la rellena.",
    "kwallet.field.public_key_optional": "Clave pública (opcional)",
    "kwallet.field.scope": "Ámbito",
    "kwallet.field.scope_fixed": "Este elemento pertenece a este stack.",
    "kwallet.field.secret": "Valor secreto",
    "kwallet.field.segment_hint":
      "Letras, dígitos, puntos, guiones y guiones bajos, hasta 64 caracteres.",
    "kwallet.field.totp": "Secreto para códigos de un solo uso (opcional)",
    "kwallet.field.type": "Tipo",
    "kwallet.field.user": "Usuario",
    "kwallet.field.username": "Nombre de usuario",
    "kwallet.field.value": "Valor",
    "kwallet.field.value_access": "Quién puede ver el valor",
    "kwallet.field.variable_name": "Nombre de la variable",
    "kwallet.field.variable_name_hint":
      "Es el nombre con el que la buscan tus herramientas, como DATABASE_URL: letras mayúsculas, dígitos y guiones bajos, sin empezar por un dígito. Único dentro del ámbito.",
    "kwallet.filter.all": "Todos",
    "kwallet.filter.no_match":
      "Ningún elemento coincide con tu búsqueda o tus filtros.",
    "kwallet.filter.scope": "Ámbito",
    "kwallet.filter.search": "Buscar",
    "kwallet.filter.search_placeholder": "Nombre, descripción o stack",
    "kwallet.filter.type": "Tipo",
    "kwallet.import.decrypt": "Descifrar archivo",
    "kwallet.import.description":
      "Añade a tu Wallet las credenciales de una exportación anterior. Cada una se cifra en este navegador.",
    "kwallet.import.error.password":
      "La contraseña es incorrecta o el archivo está dañado.",
    "kwallet.import.error.unreadable":
      "No se pudo leer este archivo. Usa una exportación de Techstack (JSON o CSV) o una exportación JSON de Bitwarden.",
    "kwallet.import.file": "Archivo de exportación",
    "kwallet.import.formats":
      "Compatibles: JSON de Techstack (también protegido con contraseña), CSV de Techstack y JSON de Bitwarden.",
    "kwallet.import.no_export":
      "Tu Wallet no se puede exportar como texto sin cifrar. Para sacar un valor, muéstralo elemento por elemento.",
    "kwallet.import.open": "Importar",
    "kwallet.import.password": "Contraseña de la exportación",
    "kwallet.import.preview": "Se pueden importar {count} de {total} entradas.",
    "kwallet.import.result": "{created} importadas, {failed} con error.",
    "kwallet.import.row.created": "Importada",
    "kwallet.import.row.name_invalid":
      "Omitida: el nombre está vacío o no es válido.",
    "kwallet.import.row.no_secret": "Omitida: no tiene valor secreto.",
    "kwallet.import.row.too_large": "Omitida: el valor supera los 64 KiB.",
    "kwallet.import.run": "Importar {count} entradas",
    "kwallet.import.title": "Importar credenciales",
    "kwallet.keygen.copied": "Copiada",
    "kwallet.keygen.copy": "Copiar clave pública",
    "kwallet.keygen.generate": "Generar un nuevo par de claves",
    "kwallet.keygen.generating": "Generando…",
    "kwallet.keygen.hint":
      "El par de claves Ed25519 se genera en este navegador. La clave privada se guarda en tu Wallet y no se envía a ningún otro sitio.",
    "kwallet.keygen.install_hint":
      "Instala esta clave pública en el servidor (en authorized_keys) o deja que Techstack la instale más tarde.",
    "kwallet.keygen.open": "Generar clave SSH",
    "kwallet.keygen.public_key": "Clave pública",
    "kwallet.lead":
      "Claves de API, inicios de sesión, variables, configuraciones y accesos a servidores en tu kombify Wallet. Los valores se cifran en este navegador antes de salir de él y solo tú puedes mostrarlos.",
    "kwallet.list_caption": "Elementos de tu Wallet",
    "kwallet.load_error_title": "No se pudo cargar tu Wallet",
    "kwallet.loading": "Cargando tu Wallet…",
    "kwallet.manage_in_cloud": "Gestionar en kombify Cloud",
    "kwallet.notice.audit_pending":
      "El cambio está guardado. Su registro de auditoría aún se está escribiendo y llegará en breve.",
    "kwallet.notice.configured": "Se guardaron los ajustes de {name}.",
    "kwallet.notice.created": "{name} se añadió a tu Wallet.",
    "kwallet.notice.deleted": "{name} se eliminó.",
    "kwallet.notice.revoked": "{name} se revocó.",
    "kwallet.notice.versioned": "{name} tiene una nueva versión.",
    "kwallet.picker.create": "Crear acceso al servidor",
    "kwallet.picker.empty":
      "Todavía no tienes ningún acceso a servidor en tu Wallet.",
    "kwallet.picker.hidden":
      "Se ocultan {count} más porque son para otro servidor o método de inicio de sesión.",
    "kwallet.picker.host_first":
      "Introduce primero el host del servidor; forma parte del acceso.",
    "kwallet.picker.host_unknown": "(host sin registrar)",
    "kwallet.picker.label": "Acceso al servidor desde tu Wallet",
    "kwallet.picker.none_fit":
      "Ningún acceso a servidor de tu Wallet encaja con este servidor y método de inicio de sesión.",
    "kwallet.picker.open_wallet": "Abrir tu Wallet",
    "kwallet.picker.placeholder": "Elegir acceso al servidor",
    "kwallet.picker.rebind_label": "Usar otro acceso a servidor (opcional)",
    "kwallet.picker.techstack_notice":
      "Techstack ya puede usar este elemento para llegar a tu servidor. Puedes retirarlo en tu Wallet.",
    "kwallet.prompt.create.allow": "Permitir que Techstack guarde credenciales",
    "kwallet.prompt.create.body":
      "Para configurarlo, Techstack tiene que guardar credenciales en tu kombify Wallet. Permítelo una vez y vuelve a empezar. Puedes retirar el permiso en tu Wallet en cualquier momento.",
    "kwallet.prompt.create.done":
      "Techstack ya puede guardar credenciales en tu Wallet. Vuelve a empezar.",
    "kwallet.prompt.create.title": "Techstack necesita tu permiso",
    "kwallet.prompt.later": "Ahora no",
    "kwallet.prompt.use.allow": "Permitir que Techstack use este elemento",
    "kwallet.prompt.use.body":
      "Techstack tiene que usar este elemento de Wallet para continuar. Permítelo y vuelve a empezar. Puedes retirar el permiso en tu Wallet en cualquier momento.",
    "kwallet.prompt.use.done":
      "Techstack ya puede usar este elemento. Vuelve a empezar.",
    "kwallet.prompt.use.title": "Techstack necesita acceso a este elemento",
    "kwallet.retry": "Reintentar",
    "kwallet.reveal.again": "Mostrar de nuevo",
    "kwallet.reveal.cleared": "El valor se ha vuelto a ocultar.",
    "kwallet.reveal.copied": "Copiado",
    "kwallet.reveal.copy": "Copiar",
    "kwallet.reveal.copy_failed":
      "No se pudo copiar. Muestra el valor y cópialo a mano.",
    "kwallet.reveal.copy_named": "Copiar {field}",
    "kwallet.reveal.countdown": "Se ocultará en {seconds} s",
    "kwallet.reveal.description":
      "Se muestra solo en este navegador y se vuelve a ocultar tras 30 segundos.",
    "kwallet.reveal.hidden": "Oculto",
    "kwallet.reveal.hide": "Ocultar",
    "kwallet.reveal.loading": "Descifrando en este navegador…",
    "kwallet.reveal.show": "Ver",
    "kwallet.reveal.title": "Mostrar {name}",
    "kwallet.reveal.version": "Versión {version}",
    "kwallet.revoke.body":
      "Un elemento revocado ya no se puede mostrar ni usar. No se puede deshacer; añade un elemento nuevo para sustituirlo.",
    "kwallet.revoke.confirm": "Revocar elemento",
    "kwallet.revoke.title": "¿Revocar {name}?",
    "kwallet.rotate.hint":
      "Se recomienda rotarlo: el valor se ha entregado {count} veces desde su última versión. Añade una versión nueva para sustituirlo.",
    "kwallet.save": "Guardar en Wallet",
    "kwallet.saving": "Cifrando y guardando…",
    "kwallet.scope.account": "Cuenta",
    "kwallet.scope.environment": "Proyecto {project}, entorno {environment}",
    "kwallet.scope.project": "Proyecto {project}",
    "kwallet.scope.stack": "Stack {stack}",
    "kwallet.scope_option.account": "Toda la cuenta",
    "kwallet.scope_option.environment": "Un entorno de un proyecto",
    "kwallet.scope_option.project": "Un proyecto",
    "kwallet.settings.description":
      "Quién puede ver el valor y dónde pueden usarlo los agentes.",
    "kwallet.settings.save": "Guardar ajustes",
    "kwallet.settings.title": "Ajustes de {name}",
    "kwallet.sign.again": "Firmar otro",
    "kwallet.sign.certificate": "Certificado",
    "kwallet.sign.copy": "Copiar certificado",
    "kwallet.sign.description":
      "Pega la clave pública con la que inicias sesión. Wallet la firma para los principals y el tiempo que elijas, dentro de la política de firma de esta autoridad. Tu clave privada se queda en tu equipo.",
    "kwallet.sign.download": "Descargar",
    "kwallet.sign.download_failed":
      "No se pudo iniciar la descarga. Copia el certificado en su lugar.",
    "kwallet.sign.duration": "Válido durante",
    "kwallet.sign.duration_hint": "Como máximo {max}. Por ejemplo, 30m u 8h.",
    "kwallet.sign.error.key_invalid":
      "Pega una línea de clave pública de OpenSSH que empiece por ssh-ed25519, ecdsa-sha2-nistp256 o ssh-rsa.",
    "kwallet.sign.error.key_required": "Pega tu clave pública.",
    "kwallet.sign.error.principal_invalid":
      "«{principal}» no es un nombre válido. Los nombres de usuario llevan letras, dígitos, puntos, guiones y guiones bajos; los nombres de host, letras minúsculas, dígitos, puntos y guiones.",
    "kwallet.sign.error.principal_outside":
      "«{principal}» no está permitido por la política de firma de esta autoridad.",
    "kwallet.sign.error.principals_too_many":
      "Un certificado puede nombrar como máximo {max} principals.",
    "kwallet.sign.error.private_key":
      "Parece una clave privada. Se borró y no se envió. Pega en su lugar la clave pública (el archivo .pub).",
    "kwallet.sign.error.source_invalid":
      "Introduce direcciones IP o redes separadas por comas, como 203.0.113.7 o 198.51.100.0/24.",
    "kwallet.sign.error.source_required":
      "Introduce la dirección de origen desde la que funciona el certificado.",
    "kwallet.sign.error.ttl_invalid":
      "Introduce una duración de al menos 1m y como máximo {max}, como 30m u 8h.",
    "kwallet.sign.install_hint":
      "Guárdalo como {file} junto a tu clave privada, por ejemplo en ~/.ssh, e inicia sesión como de costumbre.",
    "kwallet.sign.kind": "Tipo de certificado",
    "kwallet.sign.kind_host":
      "Certificado de host (para que un servidor se identifique)",
    "kwallet.sign.kind_user":
      "Certificado de usuario (para iniciar sesión en un servidor)",
    "kwallet.sign.principals": "Principals",
    "kwallet.sign.principals_hint":
      "Los nombres para los que vale el certificado, separados por comas. Permitidos aquí: {allowed}",
    "kwallet.sign.public_key": "Tu clave pública",
    "kwallet.sign.public_key_hint":
      "Pega el contenido de tu archivo .pub, por ejemplo ~/.ssh/id_ed25519.pub. Sirven claves Ed25519, ECDSA P-256 y RSA. No pegues nunca una clave privada.",
    "kwallet.sign.result_principals": "Principals:",
    "kwallet.sign.source": "Dirección de origen",
    "kwallet.sign.source_hint":
      "Opcional. Las direcciones o redes desde las que funciona el certificado, separadas por comas, como 203.0.113.7 o 198.51.100.0/24.",
    "kwallet.sign.source_hint_required":
      "La política de firma lo exige. Las direcciones o redes desde las que funciona el certificado, separadas por comas, como 203.0.113.7 o 198.51.100.0/24.",
    "kwallet.sign.submit": "Firmar certificado",
    "kwallet.sign.title": "Firmar un certificado con {name}",
    "kwallet.sign.valid": "Válido desde {from} hasta {until}.",
    "kwallet.ssh_ca.copied": "Copiado",
    "kwallet.ssh_ca.copy": "Copiar",
    "kwallet.ssh_ca.copy_failed":
      "No se pudo copiar. Selecciona el texto y cópialo a mano.",
    "kwallet.ssh_ca.copy_key_named": "Copiar la clave pública de {name}",
    "kwallet.ssh_ca.error.kinds_required":
      "Elige al menos un tipo de certificado.",
    "kwallet.ssh_ca.error.max_host_invalid":
      "Introduce una duración de 1m a 366d, como 90d.",
    "kwallet.ssh_ca.error.max_user_invalid":
      "Introduce una duración de 1m a 1d, como 30m u 8h.",
    "kwallet.ssh_ca.error.principals_invalid":
      "«{principal}» no es un principal válido. Usa letras, dígitos, puntos, guiones y guiones bajos, o *. seguido de un dominio.",
    "kwallet.ssh_ca.error.principals_required":
      "Introduce al menos un principal.",
    "kwallet.ssh_ca.error.principals_too_many":
      "Una política puede incluir como máximo {max} principals.",
    "kwallet.ssh_ca.import_key":
      "Clave privada (OpenSSH, sin frase de contraseña)",
    "kwallet.ssh_ca.import_key_hint":
      "Pega una clave Ed25519 sin cifrar que empiece por -----BEGIN OPENSSH PRIVATE KEY-----.",
    "kwallet.ssh_ca.no_policy":
      "Aún no hay política de firma, así que esta autoridad no firma nada. Define una en sus ajustes.",
    "kwallet.ssh_ca.policy.description":
      "Todos los certificados que firma se mantienen dentro de estos límites.",
    "kwallet.ssh_ca.policy.duration_hint":
      "Un número y una unidad: s, m, h o d. Por ejemplo, 30m, 8h o 90d.",
    "kwallet.ssh_ca.policy.extensions":
      "Extensiones de los certificados de usuario",
    "kwallet.ssh_ca.policy.extensions_hint":
      "Solo las extensiones marcadas pueden aparecer en los certificados de usuario. permit-pty permite un terminal interactivo.",
    "kwallet.ssh_ca.policy.kind_host": "Certificados de host",
    "kwallet.ssh_ca.policy.kind_host_hint":
      "Para que los servidores demuestren quiénes son. Los agentes nunca los reciben.",
    "kwallet.ssh_ca.policy.kind_user": "Certificados de usuario",
    "kwallet.ssh_ca.policy.kind_user_hint":
      "Para las personas y los agentes que inician sesión en servidores.",
    "kwallet.ssh_ca.policy.kinds": "Tipos de certificado",
    "kwallet.ssh_ca.policy.legend": "Lo que esta autoridad puede firmar",
    "kwallet.ssh_ca.policy.max_host": "Certificado de host más largo",
    "kwallet.ssh_ca.policy.max_user": "Certificado de usuario más largo",
    "kwallet.ssh_ca.policy.principals": "Principals permitidos",
    "kwallet.ssh_ca.policy.principals_hint":
      "Uno por línea o separados por comas. Un principal es un nombre de usuario o de servidor. *.example.com representa exactamente un nombre delante de example.com, como web1.example.com.",
    "kwallet.ssh_ca.policy.require_source": "Exigir una dirección de origen",
    "kwallet.ssh_ca.policy.require_source_hint":
      "Cada certificado de usuario deberá indicar las direcciones desde las que funciona.",
    "kwallet.ssh_ca.public_key": "Clave pública",
    "kwallet.ssh_ca.public_key_hint":
      "Los servidores confían en esta clave pública para aceptar los certificados que firma esta autoridad; kombify StackKits la instala cuando configura un servidor.",
    "kwallet.ssh_ca.settings_description":
      "Lo que esta autoridad puede firmar. Un cambio se aplica desde el siguiente certificado.",
    "kwallet.ssh_ca.source.generate": "Crearla en Wallet (recomendado)",
    "kwallet.ssh_ca.source.generate_hint":
      "Wallet genera una clave Ed25519 nueva y la conserva. La clave nunca llega a este navegador ni a kombify Cloud.",
    "kwallet.ssh_ca.source.import": "Importar una clave existente",
    "kwallet.ssh_ca.source.import_hint":
      "Para una autoridad Ed25519 que ya usas. La clave se cifra en este navegador antes de enviarse.",
    "kwallet.ssh_ca.source_label": "Clave de la autoridad",
    "kwallet.stack.description":
      "Las credenciales de este stack se guardan en tu kombify Wallet. Techstack puede usar las que permitas.",
    "kwallet.stack.title": "Credenciales en tu Wallet",
    "kwallet.state.active": "Activo",
    "kwallet.state.deleted": "Eliminado",
    "kwallet.state.revoked": "Revocado",
    "kwallet.techstack.allow_hint":
      "Techstack podrá usar entonces este elemento por ti, por ejemplo para llegar a un servidor, sin que estés presente. Nunca recibe tus demás elementos.",
    "kwallet.techstack.allow_label": "Techstack puede usar este elemento",
    "kwallet.techstack.grant_failed":
      "El elemento se guardó, pero no se pudo permitir que Techstack lo use. Activa «Techstack puede usar este elemento» para el elemento.",
    "kwallet.techstack.toggle_named": "Techstack puede usar {name}",
    "kwallet.title": "Wallet",
    "kwallet.type.ai_credential": "Clave de proveedor de IA",
    "kwallet.type.configuration": "Configuración",
    "kwallet.type.generic_secret": "Clave de API, token o certificado",
    "kwallet.type.login": "Inicio de sesión",
    "kwallet.type.node_access_profile": "Acceso a servidor",
    "kwallet.type.other": "Otro elemento",
    "kwallet.type.parent_credential": "Credencial principal",
    "kwallet.type.ssh_ca": "Autoridad de certificación SSH",
    "kwallet.type.variable": "Variable",
    "kwallet.type_hint.configuration":
      "Un documento, como un archivo .env o un archivo de configuración.",
    "kwallet.type_hint.generic_secret":
      "Un valor secreto, como una clave de API, un token o un certificado.",
    "kwallet.type_hint.login":
      "Un nombre de usuario y una contraseña, con un secreto opcional para códigos de un solo uso.",
    "kwallet.type_hint.node_access_profile":
      "Cómo llegar a un servidor: host, usuario y una clave privada SSH o una contraseña.",
    "kwallet.type_hint.ssh_ca":
      "Firma certificados SSH de corta duración para ti, tus agentes y tus servidores. Su clave nunca sale de Wallet.",
    "kwallet.type_hint.variable":
      "Un único valor, como un ajuste o un identificador.",
    "kwallet.value_access.read_right_holders":
      "Los agentes y las personas con acceso de lectura pueden ver el valor",
    "kwallet.value_access.read_right_holders_hint":
      "Puedes mostrarlo en cualquier momento. Un agente solo lo ve con acceso de lectura.",
    "kwallet.value_access.use_only": "Nadie ve el valor, solo se puede usar",
    "kwallet.value_access.use_only_hint":
      "También te lo oculta a ti: ni siquiera tú puedes mostrarlo. Los agentes con acceso de uso solo pueden actuar con él, por ejemplo en una llamada a una API.",
    "kwallet.version.description":
      "El nuevo valor pasa a ser la versión actual. Se cifra en este navegador antes de salir de él.",
    "kwallet.version.save": "Guardar nueva versión",
    "kwallet.version.title": "Nueva versión de {name}",
    "kwallet.wizard.hint":
      "La credencial SSH se guarda en tu kombify Wallet, no en Techstack. Techstack puede usar el elemento que elijas.",
    "kwallet.wizard.item_required":
      "Elige o crea el acceso al servidor en tu Wallet.",
    "kwallet.working": "Procesando…",
  },
  "zh-Hans": {
    "kwallet.denied.step_up_required": "需要新的第二重验证。请确认后重试。",
    "kwallet.step_up.body":
      "查看值或签发证书需要你的第二重验证。请确认后继续。",
    "kwallet.step_up.body_minutes":
      "查看值或签发证书需要你在最近 {minutes} 分钟内完成的第二重验证。请确认后继续。",
    "kwallet.step_up.confirm": "使用第二重验证确认",
    "kwallet.step_up.unavailable":
      "无法在此窗口中确认第二重验证。请在单独的浏览器标签页中打开 Techstack 后重试。",
    "kwallet.step_up.working": "正在打开登录…",
    "kwallet.action.delete": "删除",
    "kwallet.action.delete_named": "删除 {name}",
    "kwallet.action.new_version": "新版本",
    "kwallet.action.new_version_named": "{name} 的新版本",
    "kwallet.action.reveal": "查看",
    "kwallet.action.reveal_named": "查看 {name}",
    "kwallet.action.revoke": "撤销",
    "kwallet.action.revoke_named": "撤销 {name}",
    "kwallet.action.settings": "设置",
    "kwallet.action.settings_named": "{name} 的设置",
    "kwallet.action.sign": "签发证书",
    "kwallet.action.sign_named": "用 {name} 签发证书",
    "kwallet.add": "添加条目",
    "kwallet.api.wallet_create_right_missing":
      "Techstack 需要你的许可才能把凭据保存到你的 kombify Wallet。请允许后重试。",
    "kwallet.api.wallet_item_credential_mismatch":
      "此 Wallet 条目不包含所选登录方式（SSH 密钥或密码）的凭据。请选择其他条目或更改方式。",
    "kwallet.api.wallet_item_host_mismatch":
      "此 Wallet 条目中的主机不是该服务器的主机。请选择对应此主机的条目，或创建新条目。",
    "kwallet.api.wallet_item_invalid":
      "这不是有效的 Wallet 条目。请从列表中选择。",
    "kwallet.api.wallet_item_no_public_key":
      "此服务器访问中没有可安装的公钥。请把密钥的公钥行添加到条目中，或使用在你的 Wallet 中生成的密钥对。",
    "kwallet.api.wallet_item_not_found":
      "此条目已不在你的 Wallet 中。请选择其他条目。",
    "kwallet.api.wallet_item_not_node_access":
      "此条目不是服务器访问。请选择服务器访问条目。",
    "kwallet.api.wallet_item_required":
      "请从你的 kombify Wallet 中选择服务器访问条目。Techstack 在此不接受密码或密钥标签。",
    "kwallet.api.wallet_item_unsupported":
      "此安装不使用 kombify Wallet，因此无法在这里使用 Wallet 条目。",
    "kwallet.api.wallet_item_unusable":
      "此 Wallet 条目已被撤销或删除，无法使用。请选择其他条目。",
    "kwallet.api.wallet_owner_ineligible":
      "你的账户暂时还不能在 Techstack 中使用 kombify Wallet。",
    "kwallet.api.wallet_response_invalid":
      "kombify Wallet 的响应 Techstack 无法使用。请重试，若问题持续请反馈。",
    "kwallet.api.wallet_right_missing":
      "Techstack 尚未被允许使用此 Wallet 条目。请在你的 Wallet 中允许后重试。",
    "kwallet.api.wallet_right_missing_use":
      "Techstack 尚不能使用此 Wallet 条目。请允许后重试。",
    "kwallet.api.wallet_self_hosted_only":
      "此操作仅在自托管的 Techstack 上可用。",
    "kwallet.api.wallet_unavailable": "kombify Wallet 暂时不可用。请稍后重试。",
    "kwallet.auth.password": "密码",
    "kwallet.auth.private_key": "SSH 私钥",
    "kwallet.badge.use_only": "仅可使用",
    "kwallet.cancel": "取消",
    "kwallet.close": "关闭",
    "kwallet.view.label": "视图",
    "kwallet.view.mark": "图标",
    "kwallet.view.tile": "磁贴",
    "kwallet.view.row": "列表",
    "kwallet.view.card": "卡片",
    "kwallet.detail.facts": "详情",
    "kwallet.detail.item": "条目",
    "kwallet.detail.use": "使用",
    "kwallet.detail.history": "历史",
    "kwallet.detail.item_missing":
      "此条目不在你的 Wallet 中。它可能已被删除，或属于另一个账户。",
    "kwallet.fact.version": "版本",
    "kwallet.fact.use_targets": "使用目标",
    "kwallet.fact.none": "无",
    "kwallet.fact.created": "创建时间",
    "kwallet.fact.last_release": "上次发放",
    "kwallet.fact.releases": "自此版本以来的发放次数",
    "kwallet.tag.rotate": "需轮换",
    "kwallet.tag.no_policy": "无签名策略",
    "kwallet.preset.access": "访问：登录和服务器访问",
    "kwallet.preset.access_hint":
      "登录、服务器访问和 SSH 证书颁发机构：让人员和 Techstack 进入你的堆栈服务器和应用的凭据。",
    "kwallet.preset.recovery": "恢复：机密和配置",
    "kwallet.preset.recovery_hint":
      "机密和配置：存放恢复材料和紧急访问凭据的地方。",
    "kwallet.col.scope": "范围",
    "kwallet.col.state": "状态",
    "kwallet.col.techstack": "Techstack",
    "kwallet.col.type": "类型",
    "kwallet.col.updated": "更新时间",
    "kwallet.create.description":
      "该值在此浏览器中加密。Techstack 永远看不到它。",
    "kwallet.create.title": "添加到你的 Wallet",
    "kwallet.creator.hint":
      "之后 Techstack 会把它为你的服务器创建的凭据（例如生成的访问密钥）添加进来。它只会使用你分配给它的条目。",
    "kwallet.creator.label": "允许 Techstack 在你的 Wallet 中保存凭据",
    "kwallet.delete.body": "该值及其所有版本都将被清除。此操作无法撤消。",
    "kwallet.delete.confirm": "永久删除",
    "kwallet.delete.title": "删除 {name}？",
    "kwallet.denied.auth_required": "请重新登录以访问你的 Wallet。",
    "kwallet.denied.capture_invalid": "Wallet 拒绝了加密的值。请重试。",
    "kwallet.denied.capture_key_untrusted":
      "无法验证 Wallet 的加密密钥。未发送任何内容。",
    "kwallet.denied.conflict": "该条目在此期间已更改。请重新加载列表后重试。",
    "kwallet.denied.generic": "Wallet 拒绝了该请求。",
    "kwallet.denied.item_action_unsupported":
      "此条目不支持该操作。SSH 证书颁发机构永远不会被显示，也没有新版本，而且只有 SSH 证书颁发机构才能签发证书。",
    "kwallet.denied.item_denied": "此条目已被撤销或删除。",
    "kwallet.denied.item_not_active":
      "此条目已被撤销或删除。你可以收回产品的访问权限，但不能授予。",
    "kwallet.denied.item_not_owned":
      "此条目不在你的 Wallet 中，因此无法更改产品访问权限。请重新加载后再试。",
    "kwallet.denied.item_unknown": "此条目已不在你的 Wallet 中。",
    "kwallet.denied.name_conflict": "此作用范围内已存在同名的变量或配置。",
    "kwallet.denied.owner_required":
      "只有你本人登录后才能管理你的 Wallet。代理和委托会话无法管理。",
    "kwallet.denied.owner_unknown":
      "你的登录信息未指明 Wallet 所有者。请退出后重新登录。",
    "kwallet.denied.replayed": "此更改已发送过。请重试。",
    "kwallet.denied.reveal_mismatch": "返回的值与此请求不匹配，已被丢弃。",
    "kwallet.denied.shredded": "此 Wallet 已被清除。",
    "kwallet.denied.sign_mismatch": "Wallet 的回应与你的请求不符，已被丢弃。",
    "kwallet.denied.sign_outside_policy":
      "该证书超出了此证书颁发机构的签发策略。请检查主体、时长和来源地址。",
    "kwallet.denied.sign_policy_invalid":
      "Wallet 拒绝了此签发策略。请检查主体和时长后再试。",
    "kwallet.denied.sign_policy_missing":
      "此证书颁发机构尚无签发策略，因此不会签发任何内容。请在其设置中设定一项。",
    "kwallet.denied.ssh_ca_invalid":
      "该证书颁发机构的密钥不是可用的未加密 Ed25519 OpenSSH 密钥。请导入有效密钥，或让 Wallet 创建一把。",
    "kwallet.denied.subject_key_invalid":
      "Wallet 无法为此公钥签名。请使用 Ed25519、ECDSA P-256 或不少于 2048 位的 RSA 密钥。",
    "kwallet.denied.too_large": "该值对 Wallet 来说太大。",
    "kwallet.denied.transport":
      "此浏览器无法加密或打开该值。请重新加载页面后重试。",
    "kwallet.denied.unavailable": "Wallet 暂时不可用。请稍后重试。",
    "kwallet.denied.value_use_only": "此条目的值仅可使用，无法显示。",
    "kwallet.empty_body":
      "添加 API 密钥、登录信息、变量、配置或服务器访问，将它们集中保存在一处。",
    "kwallet.empty_body_stack":
      "此 Stack 尚未保存任何凭据。添加一个，把它存放在你的 Wallet 中，Techstack 即可将其用于此 Stack。",
    "kwallet.empty_title": "你的 Wallet 是空的",
    "kwallet.error.ca_key_encrypted":
      "此密钥受密码短语保护。请先移除密码短语，或让 Wallet 创建新的证书颁发机构。",
    "kwallet.error.ca_key_invalid":
      "这不是 OpenSSH Ed25519 私钥。请粘贴完整密钥，包括 BEGIN 和 END 行。",
    "kwallet.error.configuration_name_invalid":
      "请使用最多 128 个小写字母、数字、点、连字符或下划线，以字母或数字开头。",
    "kwallet.error.description_invalid":
      "最多使用 500 个字符，开头和结尾不能有空格。",
    "kwallet.error.host_invalid": "请输入不含空格的主机。",
    "kwallet.error.keygen_unavailable":
      "此浏览器无法生成 Ed25519 密钥。请改为粘贴现有密钥。",
    "kwallet.error.name_invalid":
      "请使用 1 到 120 个字符，开头和结尾不能有空格。",
    "kwallet.error.port_invalid":
      "请输入 1 到 65535 之间的端口，或留空以使用 22。",
    "kwallet.error.public_key_invalid":
      "请输入一行以 ssh-ed25519、ecdsa-sha2- 或 ssh-rsa 开头的 OpenSSH 公钥。",
    "kwallet.error.required": "请填写此字段。",
    "kwallet.error.segment_invalid":
      "请使用字母、数字、点、连字符或下划线，最多 64 个字符。",
    "kwallet.error.value_too_large": "该值大于 64 KiB。",
    "kwallet.error.variable_name_invalid":
      "请使用最多 128 个大写字母、数字或下划线，不能以数字开头。",
    "kwallet.field.auth": "登录方式",
    "kwallet.field.configuration_name": "配置名",
    "kwallet.field.configuration_name_hint":
      "你的工具按此名称查找它，例如 app.env：小写字母、数字、点、连字符和下划线，以字母或数字开头。在作用范围内唯一。",
    "kwallet.field.description": "描述（可选）",
    "kwallet.field.document": "文档",
    "kwallet.field.environment": "环境",
    "kwallet.field.host": "主机",
    "kwallet.field.host_hint":
      "主机名或 IP 地址；如果端口不是 22，请一并填写端口。",
    "kwallet.field.name": "名称",
    "kwallet.field.name_hint": "会显示在列表中。切勿在此填写机密本身。",
    "kwallet.field.password": "密码",
    "kwallet.field.port": "端口（可选）",
    "kwallet.field.private_key": "SSH 私钥",
    "kwallet.field.project": "项目 ID",
    "kwallet.field.public_key": "公钥",
    "kwallet.field.public_key_hint":
      "Techstack 会把这行 OpenSSH 内容安装到服务器上，使该密钥可以登录。生成密钥时会自动填入。",
    "kwallet.field.public_key_optional": "公钥（可选）",
    "kwallet.field.scope": "范围",
    "kwallet.field.scope_fixed": "此条目属于该 Stack。",
    "kwallet.field.secret": "机密值",
    "kwallet.field.segment_hint":
      "字母、数字、点、连字符和下划线，最多 64 个字符。",
    "kwallet.field.totp": "一次性验证码密钥（可选）",
    "kwallet.field.type": "类型",
    "kwallet.field.user": "用户",
    "kwallet.field.username": "用户名",
    "kwallet.field.value": "值",
    "kwallet.field.value_access": "谁可以查看该值",
    "kwallet.field.variable_name": "变量名",
    "kwallet.field.variable_name_hint":
      "你的工具按此名称查找它，例如 DATABASE_URL：大写字母、数字和下划线，不能以数字开头。在作用范围内唯一。",
    "kwallet.filter.all": "全部",
    "kwallet.filter.no_match": "没有符合搜索或筛选条件的条目。",
    "kwallet.filter.scope": "范围",
    "kwallet.filter.search": "搜索",
    "kwallet.filter.search_placeholder": "名称、描述或 Stack",
    "kwallet.filter.type": "类型",
    "kwallet.import.decrypt": "解密文件",
    "kwallet.import.description":
      "把之前导出的凭据添加到你的 Wallet。每一项都在此浏览器中加密。",
    "kwallet.import.error.password": "密码错误或文件已损坏。",
    "kwallet.import.error.unreadable":
      "无法读取此文件。请使用 Techstack 导出文件（JSON 或 CSV）或 Bitwarden JSON 导出文件。",
    "kwallet.import.file": "导出文件",
    "kwallet.import.formats":
      "支持：Techstack JSON（含密码保护）、Techstack CSV 和 Bitwarden JSON。",
    "kwallet.import.no_export":
      "你的 Wallet 无法以明文导出。若要取出某个值，请逐项查看。",
    "kwallet.import.open": "导入",
    "kwallet.import.password": "导出密码",
    "kwallet.import.preview": "{total} 个条目中有 {count} 个可以导入。",
    "kwallet.import.result": "已导入 {created} 个，失败 {failed} 个。",
    "kwallet.import.row.created": "已导入",
    "kwallet.import.row.name_invalid": "已跳过：名称为空或不被允许。",
    "kwallet.import.row.no_secret": "已跳过：没有机密值。",
    "kwallet.import.row.too_large": "已跳过：该值超过 64 KiB。",
    "kwallet.import.run": "导入 {count} 个条目",
    "kwallet.import.title": "导入凭据",
    "kwallet.keygen.copied": "已复制",
    "kwallet.keygen.copy": "复制公钥",
    "kwallet.keygen.generate": "生成新的密钥对",
    "kwallet.keygen.generating": "正在生成…",
    "kwallet.keygen.hint":
      "Ed25519 密钥对在此浏览器中生成。私钥保存到你的 Wallet，不会发送到其他任何地方。",
    "kwallet.keygen.install_hint":
      "把此公钥安装到服务器上（authorized_keys 中），或稍后让 Techstack 安装。",
    "kwallet.keygen.open": "生成 SSH 密钥",
    "kwallet.keygen.public_key": "公钥",
    "kwallet.lead":
      "你的 kombify Wallet 中的 API 密钥、登录信息、变量、配置和服务器访问。值在离开此浏览器之前已在本地加密，且只有你可以查看。",
    "kwallet.list_caption": "你的 Wallet 中的条目",
    "kwallet.load_error_title": "无法加载你的 Wallet",
    "kwallet.loading": "正在加载你的 Wallet…",
    "kwallet.manage_in_cloud": "在 kombify Cloud 中管理",
    "kwallet.notice.audit_pending":
      "更改已保存。其审计记录仍在写入中，稍后完成。",
    "kwallet.notice.configured": "{name} 的设置已保存。",
    "kwallet.notice.created": "{name} 已添加到你的 Wallet。",
    "kwallet.notice.deleted": "{name} 已删除。",
    "kwallet.notice.revoked": "{name} 已撤销。",
    "kwallet.notice.versioned": "{name} 已有新版本。",
    "kwallet.picker.create": "创建服务器访问",
    "kwallet.picker.empty": "你的 Wallet 中还没有服务器访问。",
    "kwallet.picker.hidden":
      "另有 {count} 个因属于其他服务器或登录方式而被隐藏。",
    "kwallet.picker.host_first": "请先填写服务器主机；它是访问信息的一部分。",
    "kwallet.picker.host_unknown": "（未记录主机）",
    "kwallet.picker.label": "来自你的 Wallet 的服务器访问",
    "kwallet.picker.none_fit":
      "你的 Wallet 中没有适合此服务器和登录方式的服务器访问。",
    "kwallet.picker.open_wallet": "打开你的 Wallet",
    "kwallet.picker.placeholder": "选择服务器访问",
    "kwallet.picker.rebind_label": "使用其他服务器访问（可选）",
    "kwallet.picker.techstack_notice":
      "Techstack 现在可以使用此条目连接你的服务器。你可以在 Wallet 中撤回。",
    "kwallet.prompt.create.allow": "允许 Techstack 保存凭据",
    "kwallet.prompt.create.body":
      "要完成此设置，Techstack 需要把凭据保存到你的 kombify Wallet。请允许一次，然后重新开始。你可以随时在 Wallet 中撤回该权限。",
    "kwallet.prompt.create.done":
      "Techstack 现在可以把凭据保存到你的 Wallet。请重新开始。",
    "kwallet.prompt.create.title": "Techstack 需要你的许可",
    "kwallet.prompt.later": "暂不",
    "kwallet.prompt.use.allow": "允许 Techstack 使用此条目",
    "kwallet.prompt.use.body":
      "Techstack 需要使用此 Wallet 条目才能继续。请允许后重新开始。你可以随时在 Wallet 中撤回该权限。",
    "kwallet.prompt.use.done": "Techstack 现在可以使用此条目。请重新开始。",
    "kwallet.prompt.use.title": "Techstack 需要访问此条目",
    "kwallet.retry": "重试",
    "kwallet.reveal.again": "再次查看",
    "kwallet.reveal.cleared": "该值已再次隐藏。",
    "kwallet.reveal.copied": "已复制",
    "kwallet.reveal.copy": "复制",
    "kwallet.reveal.copy_failed": "复制失败。请显示该值并手动复制。",
    "kwallet.reveal.copy_named": "复制{field}",
    "kwallet.reveal.countdown": "{seconds} 秒后再次隐藏",
    "kwallet.reveal.description": "仅在此浏览器中显示，30 秒后再次隐藏。",
    "kwallet.reveal.hidden": "已隐藏",
    "kwallet.reveal.hide": "隐藏",
    "kwallet.reveal.loading": "正在此浏览器中解密…",
    "kwallet.reveal.show": "显示",
    "kwallet.reveal.title": "查看 {name}",
    "kwallet.reveal.version": "版本 {version}",
    "kwallet.revoke.body":
      "已撤销的条目将无法再查看或使用。此操作无法撤消；请添加新条目来替代它。",
    "kwallet.revoke.confirm": "撤销条目",
    "kwallet.revoke.title": "撤销 {name}？",
    "kwallet.rotate.hint":
      "建议轮换：自上一个版本以来，该值已被提供 {count} 次。添加新版本以替换它。",
    "kwallet.save": "保存到 Wallet",
    "kwallet.saving": "正在加密并保存…",
    "kwallet.scope.account": "账户",
    "kwallet.scope.environment": "项目 {project}，环境 {environment}",
    "kwallet.scope.project": "项目 {project}",
    "kwallet.scope.stack": "Stack {stack}",
    "kwallet.scope_option.account": "整个账户",
    "kwallet.scope_option.environment": "项目中的单个环境",
    "kwallet.scope_option.project": "单个项目",
    "kwallet.settings.description":
      "谁可以查看该值，以及代理可以在哪里使用它。",
    "kwallet.settings.save": "保存设置",
    "kwallet.settings.title": "{name} 的设置",
    "kwallet.sign.again": "再签发一张",
    "kwallet.sign.certificate": "证书",
    "kwallet.sign.copy": "复制证书",
    "kwallet.sign.description":
      "粘贴你用来登录的公钥。Wallet 会在此证书颁发机构的签发策略范围内，按你选择的主体和时长为它签名。你的私钥始终留在你的电脑上。",
    "kwallet.sign.download": "下载",
    "kwallet.sign.download_failed": "无法开始下载。请改为复制证书。",
    "kwallet.sign.duration": "有效期",
    "kwallet.sign.duration_hint": "最长 {max}。例如 30m 或 8h。",
    "kwallet.sign.error.key_invalid":
      "请粘贴一行以 ssh-ed25519、ecdsa-sha2-nistp256 或 ssh-rsa 开头的 OpenSSH 公钥。",
    "kwallet.sign.error.key_required": "请粘贴你的公钥。",
    "kwallet.sign.error.principal_invalid":
      "“{principal}”不是有效的名称。用户名由字母、数字、点、短横线和下划线组成；主机名由小写字母、数字、点和短横线组成。",
    "kwallet.sign.error.principal_outside":
      "此证书颁发机构的签发策略不允许“{principal}”。",
    "kwallet.sign.error.principals_too_many":
      "一张证书最多可列出 {max} 个主体。",
    "kwallet.sign.error.private_key":
      "这看起来像私钥。已将其清除，未发送。请改为粘贴公钥（.pub 文件）。",
    "kwallet.sign.error.source_invalid":
      "请输入用逗号分隔的 IP 地址或网络，例如 203.0.113.7 或 198.51.100.0/24。",
    "kwallet.sign.error.source_required": "请输入证书可用的来源地址。",
    "kwallet.sign.error.ttl_invalid":
      "请输入至少 1m、至多 {max} 的时长，例如 30m 或 8h。",
    "kwallet.sign.install_hint":
      "请将其与私钥放在一起，保存为 {file}，例如放在 ~/.ssh 中，然后照常登录。",
    "kwallet.sign.kind": "证书类型",
    "kwallet.sign.kind_host": "主机证书（供服务器表明身份）",
    "kwallet.sign.kind_user": "用户证书（用于登录服务器）",
    "kwallet.sign.principals": "主体（principal）",
    "kwallet.sign.principals_hint":
      "证书有效的名称，用逗号分隔。此处允许：{allowed}",
    "kwallet.sign.public_key": "你的公钥",
    "kwallet.sign.public_key_hint":
      "粘贴 .pub 文件的内容，例如 ~/.ssh/id_ed25519.pub。支持 Ed25519、ECDSA P-256 和 RSA 密钥。切勿粘贴私钥。",
    "kwallet.sign.result_principals": "主体：",
    "kwallet.sign.source": "来源地址",
    "kwallet.sign.source_hint":
      "可选。证书可用的地址或网络，用逗号分隔，例如 203.0.113.7 或 198.51.100.0/24。",
    "kwallet.sign.source_hint_required":
      "签发策略要求填写。证书可用的地址或网络，用逗号分隔，例如 203.0.113.7 或 198.51.100.0/24。",
    "kwallet.sign.submit": "签发证书",
    "kwallet.sign.title": "用 {name} 签发证书",
    "kwallet.sign.valid": "有效期从 {from} 到 {until}。",
    "kwallet.ssh_ca.copied": "已复制",
    "kwallet.ssh_ca.copy": "复制",
    "kwallet.ssh_ca.copy_failed": "复制失败。请选中文本后手动复制。",
    "kwallet.ssh_ca.copy_key_named": "复制 {name} 的公钥",
    "kwallet.ssh_ca.error.kinds_required": "请至少选择一种证书类型。",
    "kwallet.ssh_ca.error.max_host_invalid":
      "请输入 1m 到 366d 之间的时长，例如 90d。",
    "kwallet.ssh_ca.error.max_user_invalid":
      "请输入 1m 到 1d 之间的时长，例如 30m 或 8h。",
    "kwallet.ssh_ca.error.principals_invalid":
      "“{principal}”不是有效的主体。请使用字母、数字、点、短横线和下划线，或 *. 加域名。",
    "kwallet.ssh_ca.error.principals_required": "请至少输入一个主体。",
    "kwallet.ssh_ca.error.principals_too_many":
      "一项策略最多可列出 {max} 个主体。",
    "kwallet.ssh_ca.import_key": "私钥（OpenSSH，无密码短语）",
    "kwallet.ssh_ca.import_key_hint":
      "粘贴以 -----BEGIN OPENSSH PRIVATE KEY----- 开头的未加密 Ed25519 密钥。",
    "kwallet.ssh_ca.no_policy":
      "尚无签发策略，因此此证书颁发机构不会签发任何内容。请在其设置中设定一项。",
    "kwallet.ssh_ca.policy.description": "它签发的每张证书都不会超出这些限制。",
    "kwallet.ssh_ca.policy.duration_hint":
      "一个数字加一个单位：s、m、h 或 d。例如 30m、8h 或 90d。",
    "kwallet.ssh_ca.policy.extensions": "用户证书的扩展",
    "kwallet.ssh_ca.policy.extensions_hint":
      "只有勾选的扩展才能出现在用户证书中。permit-pty 允许交互式终端。",
    "kwallet.ssh_ca.policy.kind_host": "主机证书",
    "kwallet.ssh_ca.policy.kind_host_hint":
      "供服务器证明自己的身份。代理永远不会收到这类证书。",
    "kwallet.ssh_ca.policy.kind_user": "用户证书",
    "kwallet.ssh_ca.policy.kind_user_hint": "供登录服务器的人员和代理使用。",
    "kwallet.ssh_ca.policy.kinds": "证书类型",
    "kwallet.ssh_ca.policy.legend": "此证书颁发机构可以签发的内容",
    "kwallet.ssh_ca.policy.max_host": "主机证书的最长有效期",
    "kwallet.ssh_ca.policy.max_user": "用户证书的最长有效期",
    "kwallet.ssh_ca.policy.principals": "允许的主体（principal）",
    "kwallet.ssh_ca.policy.principals_hint":
      "每行一个，或用逗号分隔。主体是用户名或服务器名。*.example.com 表示 example.com 前恰好多一个名称，例如 web1.example.com。",
    "kwallet.ssh_ca.policy.require_source": "要求来源地址",
    "kwallet.ssh_ca.policy.require_source_hint":
      "这样每张用户证书都必须写明它可用的来源地址。",
    "kwallet.ssh_ca.public_key": "公钥",
    "kwallet.ssh_ca.public_key_hint":
      "服务器信任此公钥，从而接受该证书颁发机构签发的证书；kombify StackKits 在设置服务器时会安装它。",
    "kwallet.ssh_ca.settings_description":
      "此证书颁发机构可以签发的内容。更改从下一张证书起生效。",
    "kwallet.ssh_ca.source.generate": "在 Wallet 中创建（推荐）",
    "kwallet.ssh_ca.source.generate_hint":
      "Wallet 会生成并保管一把新的 Ed25519 密钥。该密钥永远不会到达此浏览器或 kombify Cloud。",
    "kwallet.ssh_ca.source.import": "导入现有密钥",
    "kwallet.ssh_ca.source.import_hint":
      "适用于你已在使用的 Ed25519 证书颁发机构。密钥在发送前会在此浏览器中加密。",
    "kwallet.ssh_ca.source_label": "证书颁发机构密钥",
    "kwallet.stack.description":
      "此 Stack 的凭据保存在你的 kombify Wallet 中。Techstack 可以使用你允许的那些。",
    "kwallet.stack.title": "你的 Wallet 中的凭据",
    "kwallet.state.active": "有效",
    "kwallet.state.deleted": "已删除",
    "kwallet.state.revoked": "已撤销",
    "kwallet.techstack.allow_hint":
      "之后 Techstack 可以在你不在场时代你使用此条目，例如连接服务器。它永远拿不到你的其他条目。",
    "kwallet.techstack.allow_label": "允许 Techstack 使用此条目",
    "kwallet.techstack.grant_failed":
      "条目已保存，但未能允许 Techstack 使用它。请为该条目开启“允许 Techstack 使用此条目”。",
    "kwallet.techstack.toggle_named": "允许 Techstack 使用 {name}",
    "kwallet.title": "Wallet",
    "kwallet.type.ai_credential": "AI 提供商密钥",
    "kwallet.type.configuration": "配置",
    "kwallet.type.generic_secret": "API 密钥、令牌或证书",
    "kwallet.type.login": "登录信息",
    "kwallet.type.node_access_profile": "服务器访问",
    "kwallet.type.other": "其他条目",
    "kwallet.type.parent_credential": "上级凭据",
    "kwallet.type.ssh_ca": "SSH 证书颁发机构",
    "kwallet.type.variable": "变量",
    "kwallet.type_hint.configuration": "一份文档，例如 .env 文件或配置文件。",
    "kwallet.type_hint.generic_secret":
      "一个机密值，例如 API 密钥、令牌或证书。",
    "kwallet.type_hint.login": "用户名和密码，可附带一次性验证码密钥。",
    "kwallet.type_hint.node_access_profile":
      "如何连接服务器：主机、用户，以及 SSH 私钥或密码。",
    "kwallet.type_hint.ssh_ca":
      "为你、你的代理和你的服务器签发短期 SSH 证书。其密钥永远不会离开 Wallet。",
    "kwallet.type_hint.variable": "一个值，例如一项设置或一个标识符。",
    "kwallet.value_access.read_right_holders":
      "拥有读取权限的代理和人员可以查看该值",
    "kwallet.value_access.read_right_holders_hint":
      "你可以随时显示它。代理只有在拥有读取权限时才能看到它。",
    "kwallet.value_access.use_only": "任何人都看不到该值，只能使用它",
    "kwallet.value_access.use_only_hint":
      "这也会对你隐藏它：连你也无法再显示它。拥有使用权限的代理只能用它执行操作，例如在 API 调用中。",
    "kwallet.version.description":
      "新值将成为当前版本。它在离开此浏览器之前已在本地加密。",
    "kwallet.version.save": "保存新版本",
    "kwallet.version.title": "{name} 的新版本",
    "kwallet.wizard.hint":
      "SSH 凭据保存在你的 kombify Wallet 中，而不是 Techstack。Techstack 可以使用你选择的条目。",
    "kwallet.wizard.item_required": "请在你的 Wallet 中选择或创建服务器访问。",
    "kwallet.working": "处理中…",
  },
  hi: {
    "kwallet.denied.step_up_required":
      "ताज़ा दूसरे फ़ैक्टर की ज़रूरत है। उसे पुष्ट करें और दोबारा कोशिश करें।",
    "kwallet.step_up.body":
      "किसी मान को दिखाने या प्रमाणपत्र पर हस्ताक्षर करने के लिए आपके दूसरे फ़ैक्टर की ज़रूरत है। जारी रखने के लिए उसे पुष्ट करें।",
    "kwallet.step_up.body_minutes":
      "किसी मान को दिखाने या प्रमाणपत्र पर हस्ताक्षर करने के लिए पिछले {minutes} मिनट के भीतर का आपका दूसरा फ़ैक्टर चाहिए। जारी रखने के लिए उसे पुष्ट करें।",
    "kwallet.step_up.confirm": "दूसरे फ़ैक्टर से पुष्टि करें",
    "kwallet.step_up.unavailable":
      "इस विंडो में दूसरे फ़ैक्टर की पुष्टि संभव नहीं है। Techstack को अपने ब्राउज़र टैब में खोलकर दोबारा कोशिश करें।",
    "kwallet.step_up.working": "साइन-इन खुल रहा है…",
    "kwallet.action.delete": "हटाएँ",
    "kwallet.action.delete_named": "{name} हटाएँ",
    "kwallet.action.new_version": "नया संस्करण",
    "kwallet.action.new_version_named": "{name} का नया संस्करण",
    "kwallet.action.reveal": "दिखाएँ",
    "kwallet.action.reveal_named": "{name} दिखाएँ",
    "kwallet.action.revoke": "रद्द करें",
    "kwallet.action.revoke_named": "{name} रद्द करें",
    "kwallet.action.settings": "सेटिंग्स",
    "kwallet.action.settings_named": "{name} की सेटिंग्स",
    "kwallet.action.sign": "प्रमाणपत्र पर हस्ताक्षर करें",
    "kwallet.action.sign_named": "{name} से प्रमाणपत्र पर हस्ताक्षर करें",
    "kwallet.add": "आइटम जोड़ें",
    "kwallet.api.wallet_create_right_missing":
      "Techstack को आपके kombify Wallet में क्रेडेंशियल सहेजने के लिए आपकी अनुमति चाहिए। अनुमति दें और फिर कोशिश करें।",
    "kwallet.api.wallet_item_credential_mismatch":
      "इस Wallet आइटम में चुनी गई साइन-इन विधि (SSH कुंजी या पासवर्ड) का क्रेडेंशियल नहीं है। कोई दूसरा आइटम चुनें या विधि बदलें।",
    "kwallet.api.wallet_item_host_mismatch":
      "इस Wallet आइटम का होस्ट इस सर्वर का होस्ट नहीं है। इस होस्ट के लिए कोई आइटम चुनें या नया बनाएँ।",
    "kwallet.api.wallet_item_invalid":
      "यह मान्य Wallet आइटम नहीं है। सूची से कोई चुनें।",
    "kwallet.api.wallet_item_no_public_key":
      "इस सर्वर एक्सेस में इंस्टॉल करने के लिए कोई सार्वजनिक कुंजी नहीं है। आइटम में कुंजी की सार्वजनिक पंक्ति जोड़ें, या अपने Wallet में बनाई गई कुंजी जोड़ी का उपयोग करें।",
    "kwallet.api.wallet_item_not_found":
      "यह आइटम अब आपके Wallet में नहीं है। कोई दूसरा चुनें।",
    "kwallet.api.wallet_item_not_node_access":
      "यह आइटम सर्वर एक्सेस नहीं है। कोई सर्वर एक्सेस आइटम चुनें।",
    "kwallet.api.wallet_item_required":
      "अपने kombify Wallet से सर्वर एक्सेस आइटम चुनें। Techstack यहाँ पासवर्ड या कुंजी लेबल स्वीकार नहीं करता।",
    "kwallet.api.wallet_item_unsupported":
      "यह इंस्टॉलेशन kombify Wallet का उपयोग नहीं करता, इसलिए यहाँ Wallet आइटम इस्तेमाल नहीं किया जा सकता।",
    "kwallet.api.wallet_item_unusable":
      "यह Wallet आइटम रद्द या हटा दिया गया है और उपयोग नहीं किया जा सकता। कोई दूसरा चुनें।",
    "kwallet.api.wallet_owner_ineligible":
      "आपका खाता अभी Techstack के साथ kombify Wallet का उपयोग नहीं कर सकता।",
    "kwallet.api.wallet_response_invalid":
      "kombify Wallet ने ऐसा उत्तर दिया जिसे Techstack उपयोग नहीं कर सकता। फिर कोशिश करें, और बना रहे तो इसकी सूचना दें।",
    "kwallet.api.wallet_right_missing":
      "Techstack को अभी इस Wallet आइटम के उपयोग की अनुमति नहीं है। अपने Wallet में अनुमति दें और फिर कोशिश करें।",
    "kwallet.api.wallet_right_missing_use":
      "Techstack अभी इस Wallet आइटम का उपयोग नहीं कर सकता। अनुमति दें और फिर कोशिश करें।",
    "kwallet.api.wallet_self_hosted_only":
      "यह कार्रवाई केवल सेल्फ-होस्टेड Techstack पर उपलब्ध है।",
    "kwallet.api.wallet_unavailable":
      "kombify Wallet अस्थायी रूप से उपलब्ध नहीं है। कुछ देर बाद फिर कोशिश करें।",
    "kwallet.auth.password": "पासवर्ड",
    "kwallet.auth.private_key": "SSH निजी कुंजी",
    "kwallet.badge.use_only": "केवल उपयोग",
    "kwallet.cancel": "रद्द करें",
    "kwallet.close": "बंद करें",
    "kwallet.view.label": "दृश्य",
    "kwallet.view.mark": "आइकन",
    "kwallet.view.tile": "टाइल",
    "kwallet.view.row": "सूची",
    "kwallet.view.card": "कार्ड",
    "kwallet.detail.facts": "विवरण",
    "kwallet.detail.item": "आइटम",
    "kwallet.detail.use": "उपयोग",
    "kwallet.detail.history": "इतिहास",
    "kwallet.detail.item_missing":
      "यह आइटम आपके Wallet में नहीं है। हो सकता है इसे हटा दिया गया हो या यह किसी दूसरे खाते का हो।",
    "kwallet.fact.version": "संस्करण",
    "kwallet.fact.use_targets": "उपयोग लक्ष्य",
    "kwallet.fact.none": "कोई नहीं",
    "kwallet.fact.created": "बनाया गया",
    "kwallet.fact.last_release": "अंतिम बार जारी",
    "kwallet.fact.releases": "इस संस्करण के बाद से जारी",
    "kwallet.tag.rotate": "बदलें",
    "kwallet.tag.no_policy": "कोई साइन नीति नहीं",
    "kwallet.preset.access": "पहुँच: लॉगिन और सर्वर पहुँच",
    "kwallet.preset.access_hint":
      "लॉगिन, सर्वर पहुँच और SSH प्रमाणपत्र प्राधिकरण: जिनसे लोग और Techstack आपके स्टैक के सर्वर और ऐप तक पहुँचते हैं।",
    "kwallet.preset.recovery": "पुनर्प्राप्ति: सीक्रेट और कॉन्फ़िगरेशन",
    "kwallet.preset.recovery_hint":
      "सीक्रेट और कॉन्फ़िगरेशन: पुनर्प्राप्ति सामग्री और आपातकालीन पहुँच के क्रेडेंशियल के लिए जगह।",
    "kwallet.col.scope": "दायरा",
    "kwallet.col.state": "स्थिति",
    "kwallet.col.techstack": "Techstack",
    "kwallet.col.type": "प्रकार",
    "kwallet.col.updated": "अपडेट किया गया",
    "kwallet.create.description":
      "मान इस ब्राउज़र में एन्क्रिप्ट होता है। Techstack इसे कभी नहीं देखता।",
    "kwallet.create.title": "अपने Wallet में जोड़ें",
    "kwallet.creator.hint":
      "इसके बाद Techstack आपके सर्वरों के लिए बनाए गए क्रेडेंशियल, जैसे जनरेट की गई एक्सेस कुंजी, जोड़ देगा। यह केवल वही आइटम उपयोग करता है जो आप इसे सौंपते हैं।",
    "kwallet.creator.label":
      "Techstack को आपके Wallet में क्रेडेंशियल सहेजने की अनुमति दें",
    "kwallet.delete.body":
      "मान और उसके सभी संस्करण मिटा दिए जाते हैं। इसे पलटा नहीं जा सकता।",
    "kwallet.delete.confirm": "स्थायी रूप से हटाएँ",
    "kwallet.delete.title": "{name} हटाएँ?",
    "kwallet.denied.auth_required":
      "अपने Wallet तक पहुँचने के लिए फिर से साइन इन करें।",
    "kwallet.denied.capture_invalid":
      "Wallet ने एन्क्रिप्ट किया गया मान अस्वीकार कर दिया। फिर से कोशिश करें।",
    "kwallet.denied.capture_key_untrusted":
      "Wallet की एन्क्रिप्शन कुंजी सत्यापित नहीं हो सकी। कुछ भी नहीं भेजा गया।",
    "kwallet.denied.conflict":
      "इस बीच आइटम बदल गया है। सूची फिर से लोड करें और दोबारा कोशिश करें।",
    "kwallet.denied.generic": "Wallet ने अनुरोध अस्वीकार कर दिया।",
    "kwallet.denied.item_action_unsupported":
      "यह आइटम इस कार्रवाई का समर्थन नहीं करता। SSH प्राधिकरण को कभी दिखाया नहीं जाता और उसका नया संस्करण नहीं बनता, और केवल SSH प्राधिकरण ही हस्ताक्षर करता है।",
    "kwallet.denied.item_denied":
      "यह आइटम रद्द कर दिया गया है या हटा दिया गया है।",
    "kwallet.denied.item_not_active":
      "यह आइटम रद्द कर दिया गया है या हटा दिया गया है। आप उत्पादों की एक्सेस वापस ले सकते हैं, पर दे नहीं सकते।",
    "kwallet.denied.item_not_owned":
      "यह आइटम आपके Wallet में नहीं है, इसलिए उत्पाद एक्सेस बदली नहीं जा सकती। फिर से लोड करें और दोबारा कोशिश करें।",
    "kwallet.denied.item_unknown": "यह आइटम अब आपके Wallet में नहीं है।",
    "kwallet.denied.name_conflict":
      "इस दायरे में इस नाम का वेरिएबल या कॉन्फ़िगरेशन पहले से मौजूद है।",
    "kwallet.denied.owner_required":
      "केवल आप, स्वयं साइन इन करके, अपना Wallet प्रबंधित कर सकते हैं। एजेंट और प्रत्यायोजित सत्र ऐसा नहीं कर सकते।",
    "kwallet.denied.owner_unknown":
      "आपके साइन-इन में Wallet के मालिक का नाम नहीं है। साइन आउट करके फिर से साइन इन करें।",
    "kwallet.denied.replayed":
      "यह बदलाव पहले ही भेजा जा चुका है। फिर से कोशिश करें।",
    "kwallet.denied.reveal_mismatch":
      "दिखाया गया मान इस अनुरोध से मेल नहीं खाता था और उसे हटा दिया गया।",
    "kwallet.denied.shredded": "यह Wallet मिटा दिया गया है।",
    "kwallet.denied.sign_mismatch":
      "Wallet का उत्तर आपके अनुरोध से मेल नहीं खाया और उसे हटा दिया गया।",
    "kwallet.denied.sign_outside_policy":
      "प्रमाणपत्र इस प्राधिकरण की हस्ताक्षर नीति के बाहर है। प्रिंसिपल, अवधि और स्रोत पता जाँचें।",
    "kwallet.denied.sign_policy_invalid":
      "Wallet ने यह हस्ताक्षर नीति अस्वीकार कर दी। प्रिंसिपल और अवधियाँ जाँचें और दोबारा कोशिश करें।",
    "kwallet.denied.sign_policy_missing":
      "इस प्राधिकरण की अभी कोई हस्ताक्षर नीति नहीं है, इसलिए यह किसी पर हस्ताक्षर नहीं करता। इसकी सेटिंग्स में एक तय करें।",
    "kwallet.denied.ssh_ca_invalid":
      "प्राधिकरण की कुंजी उपयोग योग्य बिना एन्क्रिप्शन वाली Ed25519 OpenSSH कुंजी नहीं है। मान्य कुंजी आयात करें, या Wallet से नई बनवाएँ।",
    "kwallet.denied.subject_key_invalid":
      "Wallet इस सार्वजनिक कुंजी पर हस्ताक्षर नहीं कर सकता। Ed25519, ECDSA P-256 या 2048 बिट या उससे बड़ी RSA कुंजी का उपयोग करें।",
    "kwallet.denied.too_large": "मान Wallet के लिए बहुत बड़ा है।",
    "kwallet.denied.transport":
      "यह ब्राउज़र मान को एन्क्रिप्ट या खोल नहीं सका। पेज फिर से लोड करें और दोबारा कोशिश करें।",
    "kwallet.denied.unavailable":
      "Wallet अस्थायी रूप से उपलब्ध नहीं है। थोड़ी देर में फिर से कोशिश करें।",
    "kwallet.denied.value_use_only":
      "इस आइटम का मान केवल उपयोग के लिए है और दिखाया नहीं जा सकता।",
    "kwallet.empty_body":
      "API कुंजी, लॉगिन, वेरिएबल, कॉन्फ़िगरेशन या सर्वर एक्सेस जोड़ें ताकि सब कुछ एक ही जगह रहे।",
    "kwallet.empty_body_stack":
      "इस स्टैक के लिए अभी कोई क्रेडेंशियल सहेजा नहीं गया है। एक जोड़ें ताकि वह आपके Wallet में रहे और Techstack उसे इस स्टैक के लिए उपयोग कर सके।",
    "kwallet.empty_title": "आपका Wallet खाली है",
    "kwallet.error.ca_key_encrypted":
      "यह कुंजी पासफ़्रेज़ से सुरक्षित है। पहले पासफ़्रेज़ हटाएँ, या Wallet से नया प्राधिकरण बनवाएँ।",
    "kwallet.error.ca_key_invalid":
      "यह OpenSSH Ed25519 निजी कुंजी नहीं है। BEGIN और END पंक्तियों सहित पूरी कुंजी चिपकाएँ।",
    "kwallet.error.configuration_name_invalid":
      "अधिकतम 128 छोटे अक्षर, अंक, बिंदु, हाइफ़न या अंडरस्कोर इस्तेमाल करें, अक्षर या अंक से शुरू करें।",
    "kwallet.error.description_invalid":
      "अधिकतम 500 वर्ण इस्तेमाल करें, शुरुआत या अंत में स्पेस के बिना।",
    "kwallet.error.host_invalid": "बिना स्पेस वाला होस्ट दर्ज करें।",
    "kwallet.error.keygen_unavailable":
      "यह ब्राउज़र Ed25519 कुंजी नहीं बना सकता। इसके बजाय कोई मौजूदा कुंजी पेस्ट करें।",
    "kwallet.error.name_invalid":
      "1 से 120 वर्ण इस्तेमाल करें, शुरुआत या अंत में स्पेस के बिना।",
    "kwallet.error.port_invalid":
      "1 से 65535 के बीच का पोर्ट दर्ज करें, या 22 के लिए खाली छोड़ें।",
    "kwallet.error.public_key_invalid":
      "ssh-ed25519, ecdsa-sha2- या ssh-rsa से शुरू होने वाली OpenSSH सार्वजनिक कुंजी की एक पंक्ति दर्ज करें।",
    "kwallet.error.required": "यह फ़ील्ड भरें।",
    "kwallet.error.segment_invalid":
      "अक्षर, अंक, बिंदु, डैश या अंडरस्कोर इस्तेमाल करें, अधिकतम 64 वर्ण।",
    "kwallet.error.value_too_large": "मान 64 KiB से बड़ा है।",
    "kwallet.error.variable_name_invalid":
      "अधिकतम 128 बड़े अक्षर, अंक या अंडरस्कोर इस्तेमाल करें, अंक से शुरू न करें।",
    "kwallet.field.auth": "साइन-इन का तरीका",
    "kwallet.field.configuration_name": "कॉन्फ़िगरेशन का नाम",
    "kwallet.field.configuration_name_hint":
      "आपके टूल इसे इसी नाम से ढूँढते हैं, जैसे app.env: छोटे अक्षर, अंक, बिंदु, हाइफ़न और अंडरस्कोर, अक्षर या अंक से शुरू। दायरे में अद्वितीय।",
    "kwallet.field.description": "विवरण (वैकल्पिक)",
    "kwallet.field.document": "दस्तावेज़",
    "kwallet.field.environment": "एनवायरनमेंट",
    "kwallet.field.host": "होस्ट",
    "kwallet.field.host_hint":
      "होस्ट नाम या IP पता, पोर्ट के साथ यदि वह 22 नहीं है।",
    "kwallet.field.name": "नाम",
    "kwallet.field.name_hint":
      "सूचियों में दिखता है। यहाँ कभी भी सीक्रेट खुद न लिखें।",
    "kwallet.field.password": "पासवर्ड",
    "kwallet.field.port": "पोर्ट (वैकल्पिक)",
    "kwallet.field.private_key": "SSH निजी कुंजी",
    "kwallet.field.project": "प्रोजेक्ट ID",
    "kwallet.field.public_key": "सार्वजनिक कुंजी",
    "kwallet.field.public_key_hint":
      "वह OpenSSH पंक्ति जिसे Techstack सर्वर पर इंस्टॉल करता है ताकि यह कुंजी लॉगिन कर सके। जनरेट की गई कुंजी इसे अपने आप भर देती है।",
    "kwallet.field.public_key_optional": "सार्वजनिक कुंजी (वैकल्पिक)",
    "kwallet.field.scope": "दायरा",
    "kwallet.field.scope_fixed": "यह आइटम इसी स्टैक का है।",
    "kwallet.field.secret": "गुप्त मान",
    "kwallet.field.segment_hint":
      "अक्षर, अंक, बिंदु, डैश और अंडरस्कोर, अधिकतम 64 वर्ण।",
    "kwallet.field.totp": "वन-टाइम कोड सीक्रेट (वैकल्पिक)",
    "kwallet.field.type": "प्रकार",
    "kwallet.field.user": "उपयोगकर्ता",
    "kwallet.field.username": "उपयोगकर्ता नाम",
    "kwallet.field.value": "मान",
    "kwallet.field.value_access": "मान कौन देख सकता है",
    "kwallet.field.variable_name": "वेरिएबल का नाम",
    "kwallet.field.variable_name_hint":
      "आपके टूल इसे इसी नाम से ढूँढते हैं, जैसे DATABASE_URL: बड़े अक्षर, अंक और अंडरस्कोर, अंक से शुरू नहीं। दायरे में अद्वितीय।",
    "kwallet.filter.all": "सभी",
    "kwallet.filter.no_match": "आपकी खोज या फ़िल्टर से कोई आइटम मेल नहीं खाता।",
    "kwallet.filter.scope": "दायरा",
    "kwallet.filter.search": "खोजें",
    "kwallet.filter.search_placeholder": "नाम, विवरण या स्टैक",
    "kwallet.filter.type": "प्रकार",
    "kwallet.import.decrypt": "फ़ाइल डिक्रिप्ट करें",
    "kwallet.import.description":
      "पिछले एक्सपोर्ट के क्रेडेंशियल अपने Wallet में जोड़ें। हर एक इसी ब्राउज़र में एन्क्रिप्ट होता है।",
    "kwallet.import.error.password": "पासवर्ड गलत है या फ़ाइल क्षतिग्रस्त है।",
    "kwallet.import.error.unreadable":
      "यह फ़ाइल पढ़ी नहीं जा सकी। Techstack एक्सपोर्ट (JSON या CSV) या Bitwarden JSON एक्सपोर्ट का उपयोग करें।",
    "kwallet.import.file": "एक्सपोर्ट फ़ाइल",
    "kwallet.import.formats":
      "समर्थित: Techstack JSON (पासवर्ड-सुरक्षित भी), Techstack CSV और Bitwarden JSON।",
    "kwallet.import.no_export":
      "आपका Wallet सादे टेक्स्ट में एक्सपोर्ट नहीं किया जा सकता। किसी मान को निकालने के लिए उसे आइटम-दर-आइटम दिखाएँ।",
    "kwallet.import.open": "आयात करें",
    "kwallet.import.password": "एक्सपोर्ट पासवर्ड",
    "kwallet.import.preview":
      "{total} में से {count} प्रविष्टियाँ आयात की जा सकती हैं।",
    "kwallet.import.result": "{created} आयात हुईं, {failed} विफल रहीं।",
    "kwallet.import.row.created": "आयात हुई",
    "kwallet.import.row.name_invalid":
      "छोड़ा गया: नाम खाली है या मान्य नहीं है।",
    "kwallet.import.row.no_secret": "छोड़ा गया: कोई गोपनीय मान नहीं है।",
    "kwallet.import.row.too_large": "छोड़ा गया: मान 64 KiB से बड़ा है।",
    "kwallet.import.run": "{count} प्रविष्टियाँ आयात करें",
    "kwallet.import.title": "क्रेडेंशियल आयात करें",
    "kwallet.keygen.copied": "कॉपी हो गई",
    "kwallet.keygen.copy": "सार्वजनिक कुंजी कॉपी करें",
    "kwallet.keygen.generate": "नई कुंजी जोड़ी बनाएँ",
    "kwallet.keygen.generating": "बनाई जा रही है…",
    "kwallet.keygen.hint":
      "Ed25519 कुंजी जोड़ी इसी ब्राउज़र में बनती है। निजी कुंजी आपके Wallet में सहेजी जाती है और कहीं और नहीं भेजी जाती।",
    "kwallet.keygen.install_hint":
      "इस सार्वजनिक कुंजी को सर्वर पर (authorized_keys में) इंस्टॉल करें, या बाद में Techstack से इंस्टॉल करवाएँ।",
    "kwallet.keygen.open": "SSH कुंजी बनाएँ",
    "kwallet.keygen.public_key": "सार्वजनिक कुंजी",
    "kwallet.lead":
      "आपके kombify Wallet में API कुंजियाँ, लॉगिन, वेरिएबल, कॉन्फ़िगरेशन और सर्वर एक्सेस। मान इस ब्राउज़र से बाहर जाने से पहले यहीं एन्क्रिप्ट होते हैं, और केवल आप ही उन्हें देख सकते हैं।",
    "kwallet.list_caption": "आपके Wallet के आइटम",
    "kwallet.load_error_title": "आपका Wallet लोड नहीं हो सका",
    "kwallet.loading": "आपका Wallet लोड हो रहा है…",
    "kwallet.manage_in_cloud": "kombify Cloud में प्रबंधित करें",
    "kwallet.notice.audit_pending":
      "बदलाव सहेज लिया गया है। इसका ऑडिट रिकॉर्ड अभी लिखा जा रहा है और जल्द ही पूरा होगा।",
    "kwallet.notice.configured": "{name} की सेटिंग्स सेव हो गईं।",
    "kwallet.notice.created": "{name} आपके Wallet में जोड़ा गया।",
    "kwallet.notice.deleted": "{name} हटाया गया।",
    "kwallet.notice.revoked": "{name} रद्द किया गया।",
    "kwallet.notice.versioned": "{name} का नया संस्करण है।",
    "kwallet.picker.create": "सर्वर एक्सेस बनाएँ",
    "kwallet.picker.empty": "आपके Wallet में अभी कोई सर्वर एक्सेस नहीं है।",
    "kwallet.picker.hidden":
      "{count} और छिपे हैं क्योंकि वे किसी अन्य सर्वर या साइन-इन विधि के लिए हैं।",
    "kwallet.picker.host_first":
      "पहले सर्वर का होस्ट दर्ज करें; यह एक्सेस का हिस्सा है।",
    "kwallet.picker.host_unknown": "(होस्ट दर्ज नहीं)",
    "kwallet.picker.label": "आपके Wallet से सर्वर एक्सेस",
    "kwallet.picker.none_fit":
      "आपके Wallet में कोई सर्वर एक्सेस इस सर्वर और साइन-इन विधि से मेल नहीं खाता।",
    "kwallet.picker.open_wallet": "अपना Wallet खोलें",
    "kwallet.picker.placeholder": "सर्वर एक्सेस चुनें",
    "kwallet.picker.rebind_label": "दूसरा सर्वर एक्सेस उपयोग करें (वैकल्पिक)",
    "kwallet.picker.techstack_notice":
      "Techstack अब आपके सर्वर तक पहुँचने के लिए इस आइटम का उपयोग कर सकता है। आप इसे अपने Wallet में वापस ले सकते हैं।",
    "kwallet.prompt.create.allow": "Techstack को क्रेडेंशियल सहेजने दें",
    "kwallet.prompt.create.body":
      "इसे सेट करने के लिए Techstack को आपके kombify Wallet में क्रेडेंशियल सहेजने होंगे। एक बार अनुमति दें, फिर दोबारा शुरू करें। आप यह अनुमति कभी भी अपने Wallet में वापस ले सकते हैं।",
    "kwallet.prompt.create.done":
      "Techstack अब आपके Wallet में क्रेडेंशियल सहेज सकता है। दोबारा शुरू करें।",
    "kwallet.prompt.create.title": "Techstack को आपकी अनुमति चाहिए",
    "kwallet.prompt.later": "अभी नहीं",
    "kwallet.prompt.use.allow": "Techstack को इस आइटम का उपयोग करने दें",
    "kwallet.prompt.use.body":
      "आगे बढ़ने के लिए Techstack को इस Wallet आइटम का उपयोग करना होगा। अनुमति दें, फिर दोबारा शुरू करें। आप यह अनुमति कभी भी अपने Wallet में वापस ले सकते हैं।",
    "kwallet.prompt.use.done":
      "Techstack अब इस आइटम का उपयोग कर सकता है। दोबारा शुरू करें।",
    "kwallet.prompt.use.title": "Techstack को इस आइटम तक पहुँच चाहिए",
    "kwallet.retry": "फिर से कोशिश करें",
    "kwallet.reveal.again": "फिर से दिखाएँ",
    "kwallet.reveal.cleared": "मान फिर से छिपा दिया गया।",
    "kwallet.reveal.copied": "कॉपी हो गया",
    "kwallet.reveal.copy": "कॉपी करें",
    "kwallet.reveal.copy_failed":
      "कॉपी नहीं हो सका। मान दिखाएँ और उसे हाथ से कॉपी करें।",
    "kwallet.reveal.copy_named": "{field} कॉपी करें",
    "kwallet.reveal.countdown": "{seconds} सेकंड में फिर छिप जाएगा",
    "kwallet.reveal.description":
      "केवल इसी ब्राउज़र में दिखाया जाता है और 30 सेकंड बाद फिर छिप जाता है।",
    "kwallet.reveal.hidden": "छिपा हुआ",
    "kwallet.reveal.hide": "छिपाएँ",
    "kwallet.reveal.loading": "इस ब्राउज़र में डिक्रिप्ट किया जा रहा है…",
    "kwallet.reveal.show": "दिखाएँ",
    "kwallet.reveal.title": "{name} दिखाएँ",
    "kwallet.reveal.version": "संस्करण {version}",
    "kwallet.revoke.body":
      "रद्द किया गया आइटम अब न दिखाया जा सकता है और न इस्तेमाल किया जा सकता है। इसे पलटा नहीं जा सकता; इसकी जगह नया आइटम जोड़ें।",
    "kwallet.revoke.confirm": "आइटम रद्द करें",
    "kwallet.revoke.title": "{name} रद्द करें?",
    "kwallet.rotate.hint":
      "रोटेट करने की सलाह: पिछले संस्करण के बाद से यह मान {count} बार जारी किया गया है। इसे बदलने के लिए नया संस्करण जोड़ें।",
    "kwallet.save": "Wallet में सहेजें",
    "kwallet.saving": "एन्क्रिप्ट करके सहेजा जा रहा है…",
    "kwallet.scope.account": "खाता",
    "kwallet.scope.environment":
      "प्रोजेक्ट {project}, एनवायरनमेंट {environment}",
    "kwallet.scope.project": "प्रोजेक्ट {project}",
    "kwallet.scope.stack": "स्टैक {stack}",
    "kwallet.scope_option.account": "पूरा खाता",
    "kwallet.scope_option.environment": "किसी प्रोजेक्ट का एक एनवायरनमेंट",
    "kwallet.scope_option.project": "एक प्रोजेक्ट",
    "kwallet.settings.description":
      "मान कौन देख सकता है और एजेंट इसका उपयोग कहाँ कर सकते हैं।",
    "kwallet.settings.save": "सेटिंग्स सेव करें",
    "kwallet.settings.title": "{name} की सेटिंग्स",
    "kwallet.sign.again": "एक और पर हस्ताक्षर करें",
    "kwallet.sign.certificate": "प्रमाणपत्र",
    "kwallet.sign.copy": "प्रमाणपत्र कॉपी करें",
    "kwallet.sign.description":
      "वह सार्वजनिक कुंजी चिपकाएँ जिससे आप लॉग इन करते हैं। Wallet इस प्राधिकरण की हस्ताक्षर नीति के भीतर, आपके चुने प्रिंसिपल और समय के लिए उस पर हस्ताक्षर करता है। आपकी निजी कुंजी आपके कंप्यूटर पर ही रहती है।",
    "kwallet.sign.download": "डाउनलोड करें",
    "kwallet.sign.download_failed":
      "डाउनलोड शुरू नहीं हो सका। इसकी जगह प्रमाणपत्र कॉपी करें।",
    "kwallet.sign.duration": "इतने समय के लिए मान्य",
    "kwallet.sign.duration_hint": "अधिकतम {max}। उदाहरण के लिए 30m या 8h।",
    "kwallet.sign.error.key_invalid":
      "OpenSSH सार्वजनिक कुंजी की एक पंक्ति चिपकाएँ जो ssh-ed25519, ecdsa-sha2-nistp256 या ssh-rsa से शुरू होती हो।",
    "kwallet.sign.error.key_required": "अपनी सार्वजनिक कुंजी चिपकाएँ।",
    "kwallet.sign.error.principal_invalid":
      "“{principal}” मान्य नाम नहीं है। उपयोगकर्ता नामों में अक्षर, अंक, बिंदु, डैश और अंडरस्कोर होते हैं; होस्ट नामों में छोटे अक्षर, अंक, बिंदु और डैश।",
    "kwallet.sign.error.principal_outside":
      "“{principal}” की अनुमति इस प्राधिकरण की हस्ताक्षर नीति नहीं देती।",
    "kwallet.sign.error.principals_too_many":
      "एक प्रमाणपत्र में अधिकतम {max} प्रिंसिपल हो सकते हैं।",
    "kwallet.sign.error.private_key":
      "यह निजी कुंजी जैसी दिखती है। इसे साफ़ कर दिया गया और भेजा नहीं गया। इसकी जगह सार्वजनिक कुंजी (.pub फ़ाइल) चिपकाएँ।",
    "kwallet.sign.error.source_invalid":
      "अल्पविराम से अलग IP पते या नेटवर्क दर्ज करें, जैसे 203.0.113.7 या 198.51.100.0/24।",
    "kwallet.sign.error.source_required":
      "वह स्रोत पता दर्ज करें जिससे प्रमाणपत्र काम करता है।",
    "kwallet.sign.error.ttl_invalid":
      "कम से कम 1m और अधिकतम {max} की अवधि दर्ज करें, जैसे 30m या 8h।",
    "kwallet.sign.install_hint":
      "इसे अपनी निजी कुंजी के बगल में {file} नाम से सहेजें, जैसे ~/.ssh में, फिर हमेशा की तरह लॉग इन करें।",
    "kwallet.sign.kind": "प्रमाणपत्र का प्रकार",
    "kwallet.sign.kind_host":
      "होस्ट प्रमाणपत्र (सर्वर के अपनी पहचान बताने के लिए)",
    "kwallet.sign.kind_user":
      "उपयोगकर्ता प्रमाणपत्र (सर्वर में लॉग इन करने के लिए)",
    "kwallet.sign.principals": "प्रिंसिपल",
    "kwallet.sign.principals_hint":
      "वे नाम जिनके लिए प्रमाणपत्र मान्य है, अल्पविराम से अलग। यहाँ अनुमत: {allowed}",
    "kwallet.sign.public_key": "आपकी सार्वजनिक कुंजी",
    "kwallet.sign.public_key_hint":
      "अपनी .pub फ़ाइल की सामग्री चिपकाएँ, जैसे ~/.ssh/id_ed25519.pub। Ed25519, ECDSA P-256 और RSA कुंजियाँ काम करती हैं। निजी कुंजी कभी न चिपकाएँ।",
    "kwallet.sign.result_principals": "प्रिंसिपल:",
    "kwallet.sign.source": "स्रोत पता",
    "kwallet.sign.source_hint":
      "वैकल्पिक। वे पते या नेटवर्क जिनसे प्रमाणपत्र काम करता है, अल्पविराम से अलग, जैसे 203.0.113.7 या 198.51.100.0/24।",
    "kwallet.sign.source_hint_required":
      "हस्ताक्षर नीति के अनुसार अनिवार्य। वे पते या नेटवर्क जिनसे प्रमाणपत्र काम करता है, अल्पविराम से अलग, जैसे 203.0.113.7 या 198.51.100.0/24।",
    "kwallet.sign.submit": "प्रमाणपत्र पर हस्ताक्षर करें",
    "kwallet.sign.title": "{name} से प्रमाणपत्र पर हस्ताक्षर करें",
    "kwallet.sign.valid": "{from} से {until} तक मान्य।",
    "kwallet.ssh_ca.copied": "कॉपी हो गया",
    "kwallet.ssh_ca.copy": "कॉपी करें",
    "kwallet.ssh_ca.copy_failed":
      "कॉपी नहीं हो सका। टेक्स्ट चुनकर हाथ से कॉपी करें।",
    "kwallet.ssh_ca.copy_key_named": "{name} की सार्वजनिक कुंजी कॉपी करें",
    "kwallet.ssh_ca.error.kinds_required":
      "प्रमाणपत्र का कम से कम एक प्रकार चुनें।",
    "kwallet.ssh_ca.error.max_host_invalid":
      "1m से 366d तक की अवधि दर्ज करें, जैसे 90d।",
    "kwallet.ssh_ca.error.max_user_invalid":
      "1m से 1d तक की अवधि दर्ज करें, जैसे 30m या 8h।",
    "kwallet.ssh_ca.error.principals_invalid":
      "“{principal}” मान्य प्रिंसिपल नहीं है। अक्षर, अंक, बिंदु, डैश और अंडरस्कोर का उपयोग करें, या *. के बाद डोमेन लिखें।",
    "kwallet.ssh_ca.error.principals_required":
      "कम से कम एक प्रिंसिपल दर्ज करें।",
    "kwallet.ssh_ca.error.principals_too_many":
      "एक नीति में अधिकतम {max} प्रिंसिपल हो सकते हैं।",
    "kwallet.ssh_ca.import_key": "निजी कुंजी (OpenSSH, बिना पासफ़्रेज़ के)",
    "kwallet.ssh_ca.import_key_hint":
      "-----BEGIN OPENSSH PRIVATE KEY----- से शुरू होने वाली बिना एन्क्रिप्शन की Ed25519 कुंजी चिपकाएँ।",
    "kwallet.ssh_ca.no_policy":
      "अभी कोई हस्ताक्षर नीति नहीं है, इसलिए यह प्राधिकरण किसी पर हस्ताक्षर नहीं करता। इसकी सेटिंग्स में एक तय करें।",
    "kwallet.ssh_ca.policy.description":
      "यह जो भी प्रमाणपत्र जारी करता है, वह इन सीमाओं के भीतर ही रहता है।",
    "kwallet.ssh_ca.policy.duration_hint":
      "एक संख्या और एक इकाई: s, m, h या d। उदाहरण के लिए 30m, 8h या 90d।",
    "kwallet.ssh_ca.policy.extensions": "उपयोगकर्ता प्रमाणपत्रों के एक्सटेंशन",
    "kwallet.ssh_ca.policy.extensions_hint":
      "केवल चिह्नित एक्सटेंशन ही उपयोगकर्ता प्रमाणपत्रों में आ सकते हैं। permit-pty इंटरैक्टिव टर्मिनल की अनुमति देता है।",
    "kwallet.ssh_ca.policy.kind_host": "होस्ट प्रमाणपत्र",
    "kwallet.ssh_ca.policy.kind_host_hint":
      "सर्वरों के अपनी पहचान साबित करने के लिए। एजेंटों को ये कभी नहीं मिलते।",
    "kwallet.ssh_ca.policy.kind_user": "उपयोगकर्ता प्रमाणपत्र",
    "kwallet.ssh_ca.policy.kind_user_hint":
      "उन लोगों और एजेंटों के लिए जो सर्वरों में लॉग इन करते हैं।",
    "kwallet.ssh_ca.policy.kinds": "प्रमाणपत्र के प्रकार",
    "kwallet.ssh_ca.policy.legend": "यह प्राधिकरण किन पर हस्ताक्षर कर सकता है",
    "kwallet.ssh_ca.policy.max_host": "सबसे लंबा होस्ट प्रमाणपत्र",
    "kwallet.ssh_ca.policy.max_user": "सबसे लंबा उपयोगकर्ता प्रमाणपत्र",
    "kwallet.ssh_ca.policy.principals": "अनुमत प्रिंसिपल",
    "kwallet.ssh_ca.policy.principals_hint":
      "हर पंक्ति में एक, या अल्पविराम से अलग। प्रिंसिपल उपयोगकर्ता या सर्वर का नाम होता है। *.example.com का मतलब example.com के आगे ठीक एक नाम है, जैसे web1.example.com।",
    "kwallet.ssh_ca.policy.require_source": "स्रोत पता अनिवार्य करें",
    "kwallet.ssh_ca.policy.require_source_hint":
      "तब हर उपयोगकर्ता प्रमाणपत्र में उन पतों का उल्लेख होना चाहिए जिनसे वह काम करता है।",
    "kwallet.ssh_ca.public_key": "सार्वजनिक कुंजी",
    "kwallet.ssh_ca.public_key_hint":
      "सर्वर इस सार्वजनिक कुंजी पर भरोसा करके इस प्राधिकरण के हस्ताक्षर किए प्रमाणपत्र स्वीकार करते हैं; kombify StackKits सर्वर सेट अप करते समय इसे इंस्टॉल करता है।",
    "kwallet.ssh_ca.settings_description":
      "यह प्राधिकरण किन पर हस्ताक्षर कर सकता है। बदलाव अगले प्रमाणपत्र से लागू होता है।",
    "kwallet.ssh_ca.source.generate": "Wallet में बनाएँ (अनुशंसित)",
    "kwallet.ssh_ca.source.generate_hint":
      "Wallet एक नई Ed25519 कुंजी बनाता है और उसे अपने पास रखता है। कुंजी कभी इस ब्राउज़र या kombify Cloud तक नहीं पहुँचती।",
    "kwallet.ssh_ca.source.import": "मौजूदा कुंजी आयात करें",
    "kwallet.ssh_ca.source.import_hint":
      "उस Ed25519 प्राधिकरण के लिए जिसका आप पहले से उपयोग करते हैं। कुंजी भेजे जाने से पहले इस ब्राउज़र में एन्क्रिप्ट की जाती है।",
    "kwallet.ssh_ca.source_label": "प्राधिकरण की कुंजी",
    "kwallet.stack.description":
      "इस स्टैक के क्रेडेंशियल आपके kombify Wallet में रखे जाते हैं। Techstack उन्हीं का उपयोग कर सकता है जिनकी आप अनुमति देते हैं।",
    "kwallet.stack.title": "आपके Wallet में क्रेडेंशियल",
    "kwallet.state.active": "सक्रिय",
    "kwallet.state.deleted": "हटाया गया",
    "kwallet.state.revoked": "रद्द किया गया",
    "kwallet.techstack.allow_hint":
      "इसके बाद Techstack आपकी अनुपस्थिति में भी आपके लिए इस आइटम का उपयोग कर सकता है, जैसे सर्वर तक पहुँचने के लिए। उसे आपके दूसरे आइटम कभी नहीं मिलते।",
    "kwallet.techstack.allow_label": "Techstack इस आइटम का उपयोग कर सकता है",
    "kwallet.techstack.grant_failed":
      "आइटम सहेज लिया गया, लेकिन Techstack को इसके उपयोग की अनुमति नहीं दी जा सकी। इस आइटम के लिए “Techstack इस आइटम का उपयोग कर सकता है” चालू करें।",
    "kwallet.techstack.toggle_named": "Techstack {name} का उपयोग कर सकता है",
    "kwallet.title": "Wallet",
    "kwallet.type.ai_credential": "AI प्रदाता कुंजी",
    "kwallet.type.configuration": "कॉन्फ़िगरेशन",
    "kwallet.type.generic_secret": "API कुंजी, टोकन या प्रमाणपत्र",
    "kwallet.type.login": "लॉगिन",
    "kwallet.type.node_access_profile": "सर्वर एक्सेस",
    "kwallet.type.other": "अन्य आइटम",
    "kwallet.type.parent_credential": "मूल क्रेडेंशियल",
    "kwallet.type.ssh_ca": "SSH प्रमाणपत्र प्राधिकरण",
    "kwallet.type.variable": "वेरिएबल",
    "kwallet.type_hint.configuration":
      "एक दस्तावेज़, जैसे .env फ़ाइल या कॉन्फ़िगरेशन फ़ाइल।",
    "kwallet.type_hint.generic_secret":
      "एक गुप्त मान, जैसे API कुंजी, टोकन या प्रमाणपत्र।",
    "kwallet.type_hint.login":
      "उपयोगकर्ता नाम और पासवर्ड, वैकल्पिक वन-टाइम कोड सीक्रेट के साथ।",
    "kwallet.type_hint.node_access_profile":
      "सर्वर तक पहुँचने का तरीका: होस्ट, उपयोगकर्ता, और SSH निजी कुंजी या पासवर्ड।",
    "kwallet.type_hint.ssh_ca":
      "आपके, आपके एजेंटों और आपके सर्वरों के लिए अल्पकालिक SSH प्रमाणपत्रों पर हस्ताक्षर करता है। इसकी कुंजी Wallet से बाहर कभी नहीं जाती।",
    "kwallet.type_hint.variable": "एक मान, जैसे कोई सेटिंग या पहचानकर्ता।",
    "kwallet.value_access.read_right_holders":
      "पढ़ने की एक्सेस वाले एजेंट और लोग मान देख सकते हैं",
    "kwallet.value_access.read_right_holders_hint":
      "आप इसे कभी भी दिखा सकते हैं। एजेंट इसे केवल पढ़ने की एक्सेस होने पर देखता है।",
    "kwallet.value_access.use_only":
      "कोई भी मान नहीं देखता, इसका केवल उपयोग किया जा सकता है",
    "kwallet.value_access.use_only_hint":
      "यह इसे आपसे भी छिपा देता है: आप भी इसे नहीं दिखा सकते। उपयोग की एक्सेस वाले एजेंट केवल इसके साथ काम कर सकते हैं, जैसे किसी API कॉल में।",
    "kwallet.version.description":
      "नया मान वर्तमान संस्करण बन जाता है। यह इस ब्राउज़र से बाहर जाने से पहले यहीं एन्क्रिप्ट होता है।",
    "kwallet.version.save": "नया संस्करण सहेजें",
    "kwallet.version.title": "{name} का नया संस्करण",
    "kwallet.wizard.hint":
      "SSH क्रेडेंशियल आपके kombify Wallet में रखा जाता है, Techstack में नहीं। आप जो आइटम चुनते हैं, Techstack उसका उपयोग कर सकता है।",
    "kwallet.wizard.item_required":
      "अपने Wallet में सर्वर एक्सेस चुनें या बनाएँ।",
    "kwallet.working": "प्रक्रिया जारी है…",
  },
  ar: {
    "kwallet.denied.step_up_required":
      "يلزم عامل تحقق ثانٍ حديث. أكّده وحاول مرة أخرى.",
    "kwallet.step_up.body":
      "يتطلب إظهار قيمة أو توقيع شهادة عامل التحقق الثاني. أكّده للمتابعة.",
    "kwallet.step_up.body_minutes":
      "يتطلب إظهار قيمة أو توقيع شهادة عامل التحقق الثاني خلال آخر {minutes} دقيقة. أكّده للمتابعة.",
    "kwallet.step_up.confirm": "التأكيد بعامل التحقق الثاني",
    "kwallet.step_up.unavailable":
      "لا يمكن تأكيد عامل التحقق الثاني في هذه النافذة. افتح Techstack في تبويب متصفح مستقل وحاول مرة أخرى.",
    "kwallet.step_up.working": "جارٍ فتح تسجيل الدخول…",
    "kwallet.action.delete": "حذف",
    "kwallet.action.delete_named": "حذف {name}",
    "kwallet.action.new_version": "إصدار جديد",
    "kwallet.action.new_version_named": "إصدار جديد من {name}",
    "kwallet.action.reveal": "إظهار",
    "kwallet.action.reveal_named": "إظهار {name}",
    "kwallet.action.revoke": "إلغاء",
    "kwallet.action.revoke_named": "إلغاء {name}",
    "kwallet.action.settings": "الإعدادات",
    "kwallet.action.settings_named": "إعدادات {name}",
    "kwallet.action.sign": "توقيع شهادة",
    "kwallet.action.sign_named": "توقيع شهادة باستخدام {name}",
    "kwallet.add": "إضافة عنصر",
    "kwallet.api.wallet_create_right_missing":
      "يحتاج Techstack إلى إذنك لحفظ بيانات الاعتماد في kombify Wallet الخاصة بك. اسمح بذلك ثم حاول مرة أخرى.",
    "kwallet.api.wallet_item_credential_mismatch":
      "لا يحتوي عنصر Wallet هذا على بيانات الاعتماد لطريقة تسجيل الدخول المختارة (مفتاح SSH أو كلمة مرور). اختر عنصرًا آخر أو غيّر الطريقة.",
    "kwallet.api.wallet_item_host_mismatch":
      "المضيف في عنصر Wallet هذا ليس مضيف هذا الخادم. اختر عنصرًا لهذا المضيف أو أنشئ عنصرًا جديدًا.",
    "kwallet.api.wallet_item_invalid":
      "هذا ليس عنصر Wallet صالحًا. اختر واحدًا من القائمة.",
    "kwallet.api.wallet_item_no_public_key":
      "لا يحتوي هذا الوصول إلى الخادم على مفتاح عام لتثبيته. أضف السطر العام للمفتاح إلى العنصر، أو استخدم زوج مفاتيح مُولَّدًا في Wallet الخاصة بك.",
    "kwallet.api.wallet_item_not_found":
      "لم يعد هذا العنصر في Wallet الخاصة بك. اختر عنصرًا آخر.",
    "kwallet.api.wallet_item_not_node_access":
      "هذا العنصر ليس وصولًا إلى خادم. اختر عنصر وصول إلى خادم.",
    "kwallet.api.wallet_item_required":
      "اختر عنصر وصول إلى الخادم من kombify Wallet الخاصة بك. لا يقبل Techstack هنا كلمات مرور أو تسميات مفاتيح.",
    "kwallet.api.wallet_item_unsupported":
      "لا يستخدم هذا التثبيت kombify Wallet، لذا لا يمكن استخدام عنصر Wallet هنا.",
    "kwallet.api.wallet_item_unusable":
      "تم إلغاء عنصر Wallet هذا أو حذفه ولا يمكن استخدامه. اختر عنصرًا آخر.",
    "kwallet.api.wallet_owner_ineligible":
      "لا يمكن لحسابك استخدام kombify Wallet مع Techstack بعد.",
    "kwallet.api.wallet_response_invalid":
      "ردّت kombify Wallet بطريقة لا يستطيع Techstack استخدامها. حاول مرة أخرى وأبلغ عن المشكلة إذا استمرت.",
    "kwallet.api.wallet_right_missing":
      "لا يُسمح لـ Techstack باستخدام عنصر Wallet هذا بعد. اسمح بذلك في Wallet الخاصة بك ثم حاول مرة أخرى.",
    "kwallet.api.wallet_right_missing_use":
      "لا يمكن لـ Techstack استخدام عنصر Wallet هذا بعد. اسمح بذلك ثم حاول مرة أخرى.",
    "kwallet.api.wallet_self_hosted_only":
      "هذا الإجراء متاح فقط في Techstack المستضاف ذاتيًا.",
    "kwallet.api.wallet_unavailable":
      "kombify Wallet غير متاحة مؤقتًا. حاول مرة أخرى بعد قليل.",
    "kwallet.auth.password": "كلمة المرور",
    "kwallet.auth.private_key": "مفتاح SSH الخاص",
    "kwallet.badge.use_only": "للاستخدام فقط",
    "kwallet.cancel": "إلغاء",
    "kwallet.close": "إغلاق",
    "kwallet.view.label": "العرض",
    "kwallet.view.mark": "الأيقونات",
    "kwallet.view.tile": "المربعات",
    "kwallet.view.row": "قائمة",
    "kwallet.view.card": "بطاقات",
    "kwallet.detail.facts": "التفاصيل",
    "kwallet.detail.item": "العنصر",
    "kwallet.detail.use": "الاستخدام",
    "kwallet.detail.history": "السجل",
    "kwallet.detail.item_missing":
      "هذا العنصر ليس في Wallet الخاصة بك. ربما حُذف أو يخص حسابًا آخر.",
    "kwallet.fact.version": "الإصدار",
    "kwallet.fact.use_targets": "أهداف الاستخدام",
    "kwallet.fact.none": "لا شيء",
    "kwallet.fact.created": "تاريخ الإنشاء",
    "kwallet.fact.last_release": "آخر إصدار للقيمة",
    "kwallet.fact.releases": "مرات الإصدار منذ هذا الإصدار",
    "kwallet.tag.rotate": "يحتاج تدويرًا",
    "kwallet.tag.no_policy": "لا توجد سياسة توقيع",
    "kwallet.preset.access": "الوصول: بيانات الدخول والوصول إلى الخوادم",
    "kwallet.preset.access_hint":
      "بيانات الدخول والوصول إلى الخوادم وجهات إصدار شهادات SSH: ما يتيح للأشخاص ولـ Techstack الدخول إلى خوادم وتطبيقات حزمك.",
    "kwallet.preset.recovery": "الاسترداد: الأسرار والإعدادات",
    "kwallet.preset.recovery_hint":
      "الأسرار والإعدادات: مكان مواد الاسترداد وبيانات اعتماد الطوارئ.",
    "kwallet.col.scope": "النطاق",
    "kwallet.col.state": "الحالة",
    "kwallet.col.techstack": "Techstack",
    "kwallet.col.type": "النوع",
    "kwallet.col.updated": "آخر تحديث",
    "kwallet.create.description":
      "تُشفَّر القيمة في هذا المتصفح. لا يراها Techstack أبدًا.",
    "kwallet.create.title": "إضافة إلى Wallet الخاصة بك",
    "kwallet.creator.hint":
      "عندئذٍ يضيف Techstack بيانات الاعتماد التي ينشئها لخوادمك، مثل مفتاح وصول مُولَّد. ولا يستخدم إلا العناصر التي تسندها إليه.",
    "kwallet.creator.label":
      "السماح لـ Techstack بحفظ بيانات الاعتماد في Wallet الخاصة بك",
    "kwallet.delete.body":
      "ستُمحى القيمة وجميع إصداراتها. لا يمكن التراجع عن ذلك.",
    "kwallet.delete.confirm": "حذف نهائي",
    "kwallet.delete.title": "هل تريد حذف {name}؟",
    "kwallet.denied.auth_required":
      "سجّل الدخول مجددًا للوصول إلى Wallet الخاصة بك.",
    "kwallet.denied.capture_invalid":
      "رفضت Wallet القيمة المشفّرة. حاول مرة أخرى.",
    "kwallet.denied.capture_key_untrusted":
      "تعذّر التحقق من مفتاح تشفير Wallet. لم يُرسَل أي شيء.",
    "kwallet.denied.conflict":
      "تغيّر العنصر في هذه الأثناء. أعد تحميل القائمة وحاول مرة أخرى.",
    "kwallet.denied.generic": "رفضت Wallet الطلب.",
    "kwallet.denied.item_action_unsupported":
      "لا يدعم هذا العنصر هذا الإجراء. لا تُعرض جهة إصدار SSH أبدًا ولا يكون لها إصدار جديد، وجهة إصدار SSH وحدها هي التي توقّع.",
    "kwallet.denied.item_denied": "تم إلغاء هذا العنصر أو حذفه.",
    "kwallet.denied.item_not_active":
      "تم إلغاء هذا العنصر أو حذفه. يمكنك سحب وصول المنتجات منه، لكن لا يمكنك منحه.",
    "kwallet.denied.item_not_owned":
      "هذا العنصر ليس في Wallet الخاصة بك، لذا لا يمكن تغيير وصول المنتجات. أعد التحميل وحاول مرة أخرى.",
    "kwallet.denied.item_unknown":
      "لم يعد هذا العنصر موجودًا في Wallet الخاصة بك.",
    "kwallet.denied.name_conflict":
      "يوجد بالفعل متغيّر أو إعدادات بهذا الاسم في هذا النطاق.",
    "kwallet.denied.owner_required":
      "لا يمكن إدارة Wallet الخاصة بك إلا بواسطتك أنت بعد تسجيل دخولك بنفسك. لا يمكن ذلك للوكلاء أو الجلسات المفوّضة.",
    "kwallet.denied.owner_unknown":
      "لا يحدد تسجيل دخولك مالكًا لـ Wallet. سجّل الخروج ثم سجّل الدخول مجددًا.",
    "kwallet.denied.replayed": "تم إرسال هذا التغيير بالفعل. حاول مرة أخرى.",
    "kwallet.denied.reveal_mismatch":
      "لم تتطابق القيمة المُظهَرة مع هذا الطلب وتم تجاهلها.",
    "kwallet.denied.shredded": "تم محو هذه Wallet.",
    "kwallet.denied.sign_mismatch": "لم تطابق إجابة Wallet طلبك فتم تجاهلها.",
    "kwallet.denied.sign_outside_policy":
      "الشهادة خارج سياسة التوقيع الخاصة بجهة الإصدار هذه. تحقق من الأسماء (principals) والمدة وعنوان المصدر.",
    "kwallet.denied.sign_policy_invalid":
      "رفضت Wallet سياسة التوقيع هذه. تحقق من الأسماء (principals) والمدد وحاول مرة أخرى.",
    "kwallet.denied.sign_policy_missing":
      "ليست لجهة الإصدار هذه سياسة توقيع بعد، لذا لا توقّع شيئًا. عيّن سياسة في إعداداتها.",
    "kwallet.denied.ssh_ca_invalid":
      "مفتاح جهة الإصدار ليس مفتاح OpenSSH Ed25519 غير مشفّر قابلًا للاستخدام. استورد مفتاحًا صالحًا، أو دع Wallet تنشئ واحدًا.",
    "kwallet.denied.subject_key_invalid":
      "لا تستطيع Wallet توقيع هذا المفتاح العام. استخدم مفتاح Ed25519 أو ECDSA P-256 أو RSA بطول 2048 بت أو أكثر.",
    "kwallet.denied.too_large": "القيمة كبيرة جدًا بالنسبة إلى Wallet.",
    "kwallet.denied.transport":
      "لم يتمكن هذا المتصفح من تشفير القيمة أو فتحها. أعد تحميل الصفحة وحاول مرة أخرى.",
    "kwallet.denied.unavailable":
      "Wallet غير متاحة مؤقتًا. حاول مرة أخرى بعد قليل.",
    "kwallet.denied.value_use_only":
      "قيمة هذا العنصر للاستخدام فقط ولا يمكن إظهارها.",
    "kwallet.empty_body":
      "أضف مفتاح API أو بيانات تسجيل دخول أو متغيرًا أو إعدادات أو وصولًا إلى خادم لتحتفظ بها في مكان واحد.",
    "kwallet.empty_body_stack":
      "لا توجد بيانات اعتماد محفوظة لهذا المكدّس بعد. أضف واحدة لتبقى في Wallet الخاصة بك، حيث يمكن لـ Techstack استخدامها لهذا المكدّس.",
    "kwallet.empty_title": "Wallet الخاصة بك فارغة",
    "kwallet.error.ca_key_encrypted":
      "هذا المفتاح محمي بعبارة مرور. أزل عبارة المرور أولًا، أو دع Wallet تنشئ جهة إصدار جديدة.",
    "kwallet.error.ca_key_invalid":
      "هذا ليس مفتاح OpenSSH Ed25519 خاصًا. الصق المفتاح كاملًا بما في ذلك سطرا BEGIN وEND.",
    "kwallet.error.configuration_name_invalid":
      "استخدم حتى 128 حرفًا صغيرًا أو رقمًا أو نقطة أو شرطة أو شرطة سفلية، يبدأ بحرف أو رقم.",
    "kwallet.error.description_invalid":
      "استخدم حتى 500 حرف، دون مسافات في البداية أو النهاية.",
    "kwallet.error.host_invalid": "أدخل مضيفًا بدون مسافات.",
    "kwallet.error.keygen_unavailable":
      "لا يستطيع هذا المتصفح إنشاء مفتاح Ed25519. الصق مفتاحًا موجودًا بدلًا من ذلك.",
    "kwallet.error.name_invalid":
      "استخدم من 1 إلى 120 حرفًا، دون مسافات في البداية أو النهاية.",
    "kwallet.error.port_invalid":
      "أدخل منفذًا بين 1 و65535، أو اتركه فارغًا لاستخدام 22.",
    "kwallet.error.public_key_invalid":
      "أدخل سطرًا واحدًا لمفتاح OpenSSH عام يبدأ بـ ssh-ed25519 أو ecdsa-sha2- أو ssh-rsa.",
    "kwallet.error.required": "املأ هذا الحقل.",
    "kwallet.error.segment_invalid":
      "استخدم أحرفًا أو أرقامًا أو نقاطًا أو شرطات أو شرطات سفلية، حتى 64 حرفًا.",
    "kwallet.error.value_too_large": "القيمة أكبر من 64 كيلوبايت.",
    "kwallet.error.variable_name_invalid":
      "استخدم حتى 128 حرفًا كبيرًا أو رقمًا أو شرطة سفلية، دون أن يبدأ برقم.",
    "kwallet.field.auth": "طريقة تسجيل الدخول",
    "kwallet.field.configuration_name": "اسم الإعدادات",
    "kwallet.field.configuration_name_hint":
      "الاسم الذي تبحث به أدواتك عنه، مثل app.env: أحرف صغيرة وأرقام ونقاط وشرطات وشرطات سفلية، يبدأ بحرف أو رقم. فريد ضمن النطاق.",
    "kwallet.field.description": "الوصف (اختياري)",
    "kwallet.field.document": "المستند",
    "kwallet.field.environment": "البيئة",
    "kwallet.field.host": "المضيف",
    "kwallet.field.host_hint": "اسم مضيف أو عنوان IP، مع المنفذ إذا لم يكن 22.",
    "kwallet.field.name": "الاسم",
    "kwallet.field.name_hint": "يظهر في القوائم. لا تضع السر نفسه هنا أبدًا.",
    "kwallet.field.password": "كلمة المرور",
    "kwallet.field.port": "المنفذ (اختياري)",
    "kwallet.field.private_key": "مفتاح SSH الخاص",
    "kwallet.field.project": "معرّف المشروع",
    "kwallet.field.public_key": "المفتاح العام",
    "kwallet.field.public_key_hint":
      "سطر OpenSSH الذي يثبّته Techstack على الخادم ليتمكن هذا المفتاح من تسجيل الدخول. يملؤه المفتاح المُولَّد تلقائيًا.",
    "kwallet.field.public_key_optional": "المفتاح العام (اختياري)",
    "kwallet.field.scope": "النطاق",
    "kwallet.field.scope_fixed": "ينتمي هذا العنصر إلى هذا المكدّس.",
    "kwallet.field.secret": "القيمة السرية",
    "kwallet.field.segment_hint":
      "أحرف وأرقام ونقاط وشرطات وشرطات سفلية، حتى 64 حرفًا.",
    "kwallet.field.totp": "سر رموز الاستخدام لمرة واحدة (اختياري)",
    "kwallet.field.type": "النوع",
    "kwallet.field.user": "المستخدم",
    "kwallet.field.username": "اسم المستخدم",
    "kwallet.field.value": "القيمة",
    "kwallet.field.value_access": "من يمكنه رؤية القيمة",
    "kwallet.field.variable_name": "اسم المتغيّر",
    "kwallet.field.variable_name_hint":
      "الاسم الذي تبحث به أدواتك عنه، مثل DATABASE_URL: أحرف كبيرة وأرقام وشرطات سفلية، دون أن يبدأ برقم. فريد ضمن النطاق.",
    "kwallet.filter.all": "الكل",
    "kwallet.filter.no_match": "لا توجد عناصر تطابق بحثك أو عوامل التصفية.",
    "kwallet.filter.scope": "النطاق",
    "kwallet.filter.search": "بحث",
    "kwallet.filter.search_placeholder": "الاسم أو الوصف أو المكدّس",
    "kwallet.filter.type": "النوع",
    "kwallet.import.decrypt": "فك تشفير الملف",
    "kwallet.import.description":
      "أضف إلى Wallet الخاصة بك بيانات الاعتماد من تصدير سابق. يُشفَّر كل عنصر في هذا المتصفح.",
    "kwallet.import.error.password": "كلمة المرور غير صحيحة أو الملف تالف.",
    "kwallet.import.error.unreadable":
      "تعذّرت قراءة هذا الملف. استخدم تصدير Techstack (JSON أو CSV) أو تصدير Bitwarden بصيغة JSON.",
    "kwallet.import.file": "ملف التصدير",
    "kwallet.import.formats":
      "المدعوم: Techstack JSON (بما في ذلك المحمي بكلمة مرور) وTechstack CSV وBitwarden JSON.",
    "kwallet.import.no_export":
      "لا يمكن تصدير Wallet الخاصة بك كنص عادي. لإخراج قيمة، أظهرها عنصرًا عنصرًا.",
    "kwallet.import.open": "استيراد",
    "kwallet.import.password": "كلمة مرور التصدير",
    "kwallet.import.preview": "يمكن استيراد {count} من أصل {total} إدخالًا.",
    "kwallet.import.result": "تم استيراد {created}، وفشل {failed}.",
    "kwallet.import.row.created": "تم الاستيراد",
    "kwallet.import.row.name_invalid": "تم التخطي: الاسم فارغ أو غير مسموح به.",
    "kwallet.import.row.no_secret": "تم التخطي: لا توجد قيمة سرية.",
    "kwallet.import.row.too_large": "تم التخطي: القيمة أكبر من 64 كيبي بايت.",
    "kwallet.import.run": "استيراد {count} إدخالًا",
    "kwallet.import.title": "استيراد بيانات الاعتماد",
    "kwallet.keygen.copied": "تم النسخ",
    "kwallet.keygen.copy": "نسخ المفتاح العام",
    "kwallet.keygen.generate": "إنشاء زوج مفاتيح جديد",
    "kwallet.keygen.generating": "جارٍ الإنشاء…",
    "kwallet.keygen.hint":
      "يُنشأ زوج مفاتيح Ed25519 في هذا المتصفح. يُحفظ المفتاح الخاص في Wallet الخاصة بك ولا يُرسل إلى أي مكان آخر.",
    "kwallet.keygen.install_hint":
      "ثبّت هذا المفتاح العام على الخادم (في authorized_keys)، أو دع Techstack يثبّته لاحقًا.",
    "kwallet.keygen.open": "إنشاء مفتاح SSH",
    "kwallet.keygen.public_key": "المفتاح العام",
    "kwallet.lead":
      "مفاتيح API وبيانات تسجيل الدخول والمتغيرات والإعدادات والوصول إلى الخوادم في kombify Wallet الخاصة بك. تُشفَّر القيم في هذا المتصفح قبل أن تغادره، ولا يمكن لأحد غيرك إظهارها.",
    "kwallet.list_caption": "العناصر في Wallet الخاصة بك",
    "kwallet.load_error_title": "تعذّر تحميل Wallet الخاصة بك",
    "kwallet.loading": "جارٍ تحميل Wallet الخاصة بك…",
    "kwallet.manage_in_cloud": "الإدارة في kombify Cloud",
    "kwallet.notice.audit_pending":
      "تم حفظ التغيير. لا يزال سجل التدقيق الخاص به قيد الكتابة وسيكتمل قريبًا.",
    "kwallet.notice.configured": "تم حفظ إعدادات {name}.",
    "kwallet.notice.created": "تمت إضافة {name} إلى Wallet الخاصة بك.",
    "kwallet.notice.deleted": "تم حذف {name}.",
    "kwallet.notice.revoked": "تم إلغاء {name}.",
    "kwallet.notice.versioned": "لدى {name} إصدار جديد.",
    "kwallet.picker.create": "إنشاء وصول إلى الخادم",
    "kwallet.picker.empty":
      "لا يوجد لديك أي وصول إلى خادم في Wallet الخاصة بك بعد.",
    "kwallet.picker.hidden":
      "تم إخفاء {count} أخرى لأنها لخادم أو طريقة تسجيل دخول مختلفة.",
    "kwallet.picker.host_first": "أدخل مضيف الخادم أولًا؛ فهو جزء من الوصول.",
    "kwallet.picker.host_unknown": "(المضيف غير مسجَّل)",
    "kwallet.picker.label": "الوصول إلى الخادم من Wallet الخاصة بك",
    "kwallet.picker.none_fit":
      "لا يوجد وصول إلى خادم في Wallet الخاصة بك يناسب هذا الخادم وطريقة تسجيل الدخول.",
    "kwallet.picker.open_wallet": "افتح Wallet الخاصة بك",
    "kwallet.picker.placeholder": "اختر الوصول إلى الخادم",
    "kwallet.picker.rebind_label": "استخدام وصول آخر إلى الخادم (اختياري)",
    "kwallet.picker.techstack_notice":
      "يمكن لـ Techstack الآن استخدام هذا العنصر للوصول إلى خادمك. يمكنك سحب ذلك في Wallet الخاصة بك.",
    "kwallet.prompt.create.allow": "السماح لـ Techstack بحفظ بيانات الاعتماد",
    "kwallet.prompt.create.body":
      "لإعداد ذلك، يحتاج Techstack إلى حفظ بيانات اعتماد في kombify Wallet الخاصة بك. اسمح بذلك مرة واحدة ثم ابدأ من جديد. يمكنك سحب الإذن في Wallet الخاصة بك في أي وقت.",
    "kwallet.prompt.create.done":
      "يمكن لـ Techstack الآن حفظ بيانات الاعتماد في Wallet الخاصة بك. ابدأ من جديد.",
    "kwallet.prompt.create.title": "يحتاج Techstack إلى إذنك",
    "kwallet.prompt.later": "ليس الآن",
    "kwallet.prompt.use.allow": "السماح لـ Techstack باستخدام هذا العنصر",
    "kwallet.prompt.use.body":
      "يحتاج Techstack إلى استخدام عنصر Wallet هذا للمتابعة. اسمح بذلك ثم ابدأ من جديد. يمكنك سحب الإذن في Wallet الخاصة بك في أي وقت.",
    "kwallet.prompt.use.done":
      "يمكن لـ Techstack الآن استخدام هذا العنصر. ابدأ من جديد.",
    "kwallet.prompt.use.title": "يحتاج Techstack إلى الوصول إلى هذا العنصر",
    "kwallet.retry": "إعادة المحاولة",
    "kwallet.reveal.again": "الإظهار مجددًا",
    "kwallet.reveal.cleared": "تم إخفاء القيمة مجددًا.",
    "kwallet.reveal.copied": "تم النسخ",
    "kwallet.reveal.copy": "نسخ",
    "kwallet.reveal.copy_failed": "فشل النسخ. اعرض القيمة وانسخها يدويًا.",
    "kwallet.reveal.copy_named": "نسخ {field}",
    "kwallet.reveal.countdown": "سيُخفى مجددًا خلال {seconds} ث",
    "kwallet.reveal.description":
      "يُعرض في هذا المتصفح فقط ويُخفى مجددًا بعد 30 ثانية.",
    "kwallet.reveal.hidden": "مخفي",
    "kwallet.reveal.hide": "إخفاء",
    "kwallet.reveal.loading": "جارٍ فك التشفير في هذا المتصفح…",
    "kwallet.reveal.show": "عرض",
    "kwallet.reveal.title": "إظهار {name}",
    "kwallet.reveal.version": "الإصدار {version}",
    "kwallet.revoke.body":
      "لا يمكن إظهار العنصر الملغى أو استخدامه بعد الآن. لا يمكن التراجع عن ذلك؛ أضف عنصرًا جديدًا ليحل محله.",
    "kwallet.revoke.confirm": "إلغاء العنصر",
    "kwallet.revoke.title": "هل تريد إلغاء {name}؟",
    "kwallet.rotate.hint":
      "يُنصح بالتدوير: أُفرج عن القيمة {count} مرة منذ آخر إصدار لها. أضف إصدارًا جديدًا لاستبدالها.",
    "kwallet.save": "حفظ في Wallet",
    "kwallet.saving": "جارٍ التشفير والحفظ…",
    "kwallet.scope.account": "الحساب",
    "kwallet.scope.environment": "المشروع {project}، البيئة {environment}",
    "kwallet.scope.project": "المشروع {project}",
    "kwallet.scope.stack": "المكدّس {stack}",
    "kwallet.scope_option.account": "الحساب بالكامل",
    "kwallet.scope_option.environment": "بيئة واحدة من مشروع",
    "kwallet.scope_option.project": "مشروع واحد",
    "kwallet.settings.description":
      "من يمكنه رؤية القيمة وأين يمكن للوكلاء استخدامها.",
    "kwallet.settings.save": "حفظ الإعدادات",
    "kwallet.settings.title": "إعدادات {name}",
    "kwallet.sign.again": "توقيع شهادة أخرى",
    "kwallet.sign.certificate": "الشهادة",
    "kwallet.sign.copy": "نسخ الشهادة",
    "kwallet.sign.description":
      "الصق المفتاح العام الذي تسجّل به الدخول. توقّعه Wallet للأسماء (principals) والمدة التي تختارها، ضمن سياسة التوقيع الخاصة بجهة الإصدار هذه. يبقى مفتاحك الخاص على حاسوبك.",
    "kwallet.sign.download": "تنزيل",
    "kwallet.sign.download_failed":
      "تعذّر بدء التنزيل. انسخ الشهادة بدلًا من ذلك.",
    "kwallet.sign.duration": "صالحة لمدة",
    "kwallet.sign.duration_hint": "{max} على الأكثر. مثلًا 30m أو 8h.",
    "kwallet.sign.error.key_invalid":
      "الصق سطرًا واحدًا لمفتاح OpenSSH عام يبدأ بـ ssh-ed25519 أو ecdsa-sha2-nistp256 أو ssh-rsa.",
    "kwallet.sign.error.key_required": "الصق مفتاحك العام.",
    "kwallet.sign.error.principal_invalid":
      "«{principal}» ليس اسمًا صالحًا. تتكون أسماء المستخدمين من حروف وأرقام ونقاط وشرطات وشرطات سفلية؛ وأسماء المضيفين من حروف صغيرة وأرقام ونقاط وشرطات.",
    "kwallet.sign.error.principal_outside":
      "لا تسمح سياسة التوقيع الخاصة بجهة الإصدار هذه بـ «{principal}».",
    "kwallet.sign.error.principals_too_many":
      "يمكن أن تذكر الشهادة {max} من الـ principals على الأكثر.",
    "kwallet.sign.error.private_key":
      "يبدو هذا مفتاحًا خاصًا. تم مسحه ولم يُرسل. الصق المفتاح العام (ملف .pub) بدلًا منه.",
    "kwallet.sign.error.source_invalid":
      "أدخل عناوين IP أو شبكات مفصولة بفواصل، مثل 203.0.113.7 أو 198.51.100.0/24.",
    "kwallet.sign.error.source_required":
      "أدخل عنوان المصدر الذي تعمل منه الشهادة.",
    "kwallet.sign.error.ttl_invalid":
      "أدخل مدة لا تقل عن 1m ولا تزيد على {max}، مثل 30m أو 8h.",
    "kwallet.sign.install_hint":
      "احفظها باسم {file} بجانب مفتاحك الخاص، مثلًا في ~/.ssh، ثم سجّل الدخول كالمعتاد.",
    "kwallet.sign.kind": "نوع الشهادة",
    "kwallet.sign.kind_host": "شهادة مضيف (ليُعرّف الخادم بنفسه)",
    "kwallet.sign.kind_user": "شهادة مستخدم (لتسجيل الدخول إلى خادم)",
    "kwallet.sign.principals": "الأسماء (principals)",
    "kwallet.sign.principals_hint":
      "الأسماء التي تصلح لها الشهادة، مفصولة بفواصل. المسموح هنا: {allowed}",
    "kwallet.sign.public_key": "مفتاحك العام",
    "kwallet.sign.public_key_hint":
      "الصق محتوى ملف .pub الخاص بك، مثل ~/.ssh/id_ed25519.pub. تعمل مفاتيح Ed25519 وECDSA P-256 وRSA. لا تلصق مفتاحًا خاصًا أبدًا.",
    "kwallet.sign.result_principals": "الأسماء (principals):",
    "kwallet.sign.source": "عنوان المصدر",
    "kwallet.sign.source_hint":
      "اختياري. العناوين أو الشبكات التي تعمل منها الشهادة، مفصولة بفواصل، مثل 203.0.113.7 أو 198.51.100.0/24.",
    "kwallet.sign.source_hint_required":
      "تشترطه سياسة التوقيع. العناوين أو الشبكات التي تعمل منها الشهادة، مفصولة بفواصل، مثل 203.0.113.7 أو 198.51.100.0/24.",
    "kwallet.sign.submit": "توقيع شهادة",
    "kwallet.sign.title": "توقيع شهادة باستخدام {name}",
    "kwallet.sign.valid": "صالحة من {from} حتى {until}.",
    "kwallet.ssh_ca.copied": "تم النسخ",
    "kwallet.ssh_ca.copy": "نسخ",
    "kwallet.ssh_ca.copy_failed": "فشل النسخ. حدّد النص وانسخه يدويًا.",
    "kwallet.ssh_ca.copy_key_named": "نسخ المفتاح العام لـ {name}",
    "kwallet.ssh_ca.error.kinds_required":
      "اختر نوعًا واحدًا على الأقل من الشهادات.",
    "kwallet.ssh_ca.error.max_host_invalid":
      "أدخل مدة من 1m إلى 366d، مثل 90d.",
    "kwallet.ssh_ca.error.max_user_invalid":
      "أدخل مدة من 1m إلى 1d، مثل 30m أو 8h.",
    "kwallet.ssh_ca.error.principals_invalid":
      "«{principal}» ليس principal صالحًا. استخدم الحروف والأرقام والنقاط والشرطات والشرطات السفلية، أو *. متبوعة بنطاق.",
    "kwallet.ssh_ca.error.principals_required":
      "أدخل اسمًا واحدًا على الأقل (principal).",
    "kwallet.ssh_ca.error.principals_too_many":
      "يمكن أن تتضمن السياسة {max} من الـ principals على الأكثر.",
    "kwallet.ssh_ca.import_key": "المفتاح الخاص (OpenSSH، بدون عبارة مرور)",
    "kwallet.ssh_ca.import_key_hint":
      "الصق مفتاح Ed25519 غير مشفّر يبدأ بـ -----BEGIN OPENSSH PRIVATE KEY-----.",
    "kwallet.ssh_ca.no_policy":
      "لا توجد سياسة توقيع بعد، لذا لا توقّع جهة الإصدار هذه شيئًا. عيّن سياسة في إعداداتها.",
    "kwallet.ssh_ca.policy.description":
      "تبقى كل شهادة توقّعها ضمن هذه الحدود.",
    "kwallet.ssh_ca.policy.duration_hint":
      "رقم ووحدة: s أو m أو h أو d. مثلًا 30m أو 8h أو 90d.",
    "kwallet.ssh_ca.policy.extensions": "امتدادات شهادات المستخدمين",
    "kwallet.ssh_ca.policy.extensions_hint":
      "لا تظهر في شهادات المستخدمين إلا الامتدادات المحدَّدة. يسمح permit-pty بطرفية تفاعلية.",
    "kwallet.ssh_ca.policy.kind_host": "شهادات المضيفين",
    "kwallet.ssh_ca.policy.kind_host_hint":
      "لتثبت الخوادم هويتها. ولا يتلقاها الوكلاء أبدًا.",
    "kwallet.ssh_ca.policy.kind_user": "شهادات المستخدمين",
    "kwallet.ssh_ca.policy.kind_user_hint":
      "للأشخاص والوكلاء الذين يسجّلون الدخول إلى الخوادم.",
    "kwallet.ssh_ca.policy.kinds": "أنواع الشهادات",
    "kwallet.ssh_ca.policy.legend": "ما يمكن لجهة الإصدار هذه توقيعه",
    "kwallet.ssh_ca.policy.max_host": "أطول مدة لشهادة مضيف",
    "kwallet.ssh_ca.policy.max_user": "أطول مدة لشهادة مستخدم",
    "kwallet.ssh_ca.policy.principals": "الأسماء المسموح بها (principals)",
    "kwallet.ssh_ca.policy.principals_hint":
      "اسم واحد في كل سطر أو مفصولة بفواصل. الـ principal اسم مستخدم أو اسم خادم. تعني *.example.com اسمًا واحدًا بالضبط قبل example.com، مثل web1.example.com.",
    "kwallet.ssh_ca.policy.require_source": "اشتراط عنوان مصدر",
    "kwallet.ssh_ca.policy.require_source_hint":
      "عندئذٍ يجب أن تذكر كل شهادة مستخدم العناوين التي تعمل منها.",
    "kwallet.ssh_ca.public_key": "المفتاح العام",
    "kwallet.ssh_ca.public_key_hint":
      "تثق الخوادم بهذا المفتاح العام لقبول الشهادات التي توقّعها جهة الإصدار هذه؛ ويثبّته kombify StackKits عند إعداد خادم.",
    "kwallet.ssh_ca.settings_description":
      "ما يمكن لجهة الإصدار هذه توقيعه. يسري التغيير من الشهادة التالية.",
    "kwallet.ssh_ca.source.generate": "إنشاؤه داخل Wallet (موصى به)",
    "kwallet.ssh_ca.source.generate_hint":
      "تنشئ Wallet مفتاح Ed25519 جديدًا وتحتفظ به. ولا يصل المفتاح أبدًا إلى هذا المتصفح أو إلى kombify Cloud.",
    "kwallet.ssh_ca.source.import": "استيراد مفتاح موجود",
    "kwallet.ssh_ca.source.import_hint":
      "لجهة إصدار Ed25519 تستخدمها بالفعل. يُشفَّر المفتاح في هذا المتصفح قبل إرساله.",
    "kwallet.ssh_ca.source_label": "مفتاح جهة الإصدار",
    "kwallet.stack.description":
      "تُحفظ بيانات اعتماد هذا المكدّس في kombify Wallet الخاصة بك. يمكن لـ Techstack استخدام ما تسمح به منها.",
    "kwallet.stack.title": "بيانات الاعتماد في Wallet الخاصة بك",
    "kwallet.state.active": "نشط",
    "kwallet.state.deleted": "محذوف",
    "kwallet.state.revoked": "ملغى",
    "kwallet.techstack.allow_hint":
      "عندئذٍ يستطيع Techstack استخدام هذا العنصر نيابةً عنك، مثل الوصول إلى خادم، دون وجودك. ولا يحصل أبدًا على عناصرك الأخرى.",
    "kwallet.techstack.allow_label": "يمكن لـ Techstack استخدام هذا العنصر",
    "kwallet.techstack.grant_failed":
      "تم حفظ العنصر، لكن تعذّر السماح لـ Techstack باستخدامه. فعّل «يمكن لـ Techstack استخدام هذا العنصر» لهذا العنصر.",
    "kwallet.techstack.toggle_named": "يمكن لـ Techstack استخدام {name}",
    "kwallet.title": "Wallet",
    "kwallet.type.ai_credential": "مفتاح مزوّد ذكاء اصطناعي",
    "kwallet.type.configuration": "إعدادات",
    "kwallet.type.generic_secret": "مفتاح API أو رمز مميز أو شهادة",
    "kwallet.type.login": "بيانات تسجيل الدخول",
    "kwallet.type.node_access_profile": "الوصول إلى خادم",
    "kwallet.type.other": "عنصر آخر",
    "kwallet.type.parent_credential": "بيانات اعتماد رئيسية",
    "kwallet.type.ssh_ca": "جهة إصدار شهادات SSH",
    "kwallet.type.variable": "متغير",
    "kwallet.type_hint.configuration": "مستند، مثل ملف ‎.env أو ملف إعدادات.",
    "kwallet.type_hint.generic_secret":
      "قيمة سرية واحدة، مثل مفتاح API أو رمز مميز أو شهادة.",
    "kwallet.type_hint.login":
      "اسم مستخدم وكلمة مرور، مع سر اختياري لرموز الاستخدام لمرة واحدة.",
    "kwallet.type_hint.node_access_profile":
      "طريقة الوصول إلى خادم: المضيف والمستخدم ومفتاح SSH خاص أو كلمة مرور.",
    "kwallet.type_hint.ssh_ca":
      "يوقّع شهادات SSH قصيرة العمر لك ولوكلائك ولخوادمك. ولا يغادر مفتاحه Wallet أبدًا.",
    "kwallet.type_hint.variable": "قيمة واحدة، مثل إعداد أو معرّف.",
    "kwallet.value_access.read_right_holders":
      "يمكن للوكلاء والأشخاص الذين لديهم وصول القراءة رؤية القيمة",
    "kwallet.value_access.read_right_holders_hint":
      "يمكنك إظهارها في أي وقت. لا يراها الوكيل إلا إذا كان لديه وصول القراءة.",
    "kwallet.value_access.use_only": "لا أحد يرى القيمة، يمكن استخدامها فقط",
    "kwallet.value_access.use_only_hint":
      "يخفيها هذا عنك أيضًا: حتى أنت لا يمكنك إظهارها. يمكن للوكلاء الذين لديهم وصول الاستخدام التصرف بها فقط، مثلًا في استدعاء API.",
    "kwallet.version.description":
      "تصبح القيمة الجديدة هي الإصدار الحالي. تُشفَّر في هذا المتصفح قبل أن تغادره.",
    "kwallet.version.save": "حفظ الإصدار الجديد",
    "kwallet.version.title": "إصدار جديد من {name}",
    "kwallet.wizard.hint":
      "تُحفظ بيانات اعتماد SSH في kombify Wallet الخاصة بك وليس في Techstack. يُسمح لـ Techstack باستخدام العنصر الذي تختاره.",
    "kwallet.wizard.item_required":
      "اختر الوصول إلى الخادم في Wallet الخاصة بك أو أنشئه.",
    "kwallet.working": "جارٍ التنفيذ…",
  },
} as const;
