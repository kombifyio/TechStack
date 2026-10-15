using System.Text.Json;
using Microsoft.Web.WebView2.Core;

namespace Kombify.TechStack.Client;

internal sealed partial class ClientWindow
{
    private CancellationTokenSource? _walletRevealCancellation;
    private string? _walletRevealRequestId;
    private sealed record WalletRevealRequest(string Type, string RequestId, long DocumentRevision,
        string DocumentToken, string ItemId, string Reason);

    private void RegisterWalletRevealBridge()
    {
        FormClosed += (_, _) => _walletRevealCancellation?.Cancel();
        _webView.CoreWebView2.NavigationStarting += (_, args) =>
        {
            if (!args.Cancel) _walletRevealCancellation?.Cancel();
        };
        _webView.CoreWebView2.WebMessageReceived += OnWalletRevealRequested;
    }

    private bool IsLocalWalletDocument(string source, long revision, string token)
    {
        if (IsDisposed || _webView.CoreWebView2 is null) return false;
        // The ordinary document bridge also permits selected remote profiles.
        // Secret disclosure must independently select the installed local runtime.
        var authority = _config.LocalUiUri();
        return DesktopEdition.IsLocal && IsLocalMode() && _boundServerProfile is null
            && _runtimeProcess is not { HasExited: true } && authority is { IsLoopback: true, Scheme: "http" }
            && authority.AbsolutePath == "/" && authority.UserInfo.Length == 0 && authority.Query.Length == 0 && authority.Fragment.Length == 0
            && Uri.TryCreate(_config.InitialUrl(), UriKind.Absolute, out var selected)
            && ClientAuthority.SameOrigin(selected, authority)
            && Uri.TryCreate(source, UriKind.Absolute, out var requested) && ClientAuthority.SameOrigin(requested, authority)
            && Uri.TryCreate(_webView.CoreWebView2.Source, UriKind.Absolute, out var current) && ClientAuthority.SameOrigin(current, authority)
            && IsCurrentDeviceContextDocument(source, revision, token);
    }

    private async Task<LocalWalletSession?> ReadWalletSession(string source, long revision, string token, CancellationToken ct)
    {
        if (!IsLocalWalletDocument(source, revision, token)) return null;
        var authority = _config.LocalUiUri()!;
        var cookies = await _webView.CoreWebView2.CookieManager.GetCookiesAsync(authority.ToString());
        ct.ThrowIfCancellationRequested();
        if (!IsLocalWalletDocument(source, revision, token)) return null;
        var pair = cookies.Where(c => c.Name is "techstack_session" or "techstack_session_origin").ToArray();
        if (pair.Length != 2 || pair.Select(c => c.Name).Distinct(StringComparer.Ordinal).Count() != 2
            || pair.Any(c => c.Domain != authority.Host || c.Path != "/" || c.IsSecure || !c.IsHttpOnly
                || c.SameSite != CoreWebView2CookieSameSiteKind.Lax || (!c.IsSession && c.Expires <= DateTime.Now) || string.IsNullOrEmpty(c.Value)))
            return null;
        var csrf = cookies.Where(c => c.Name == "csrf_token" && c.Domain == authority.Host && c.Path == "/" && !c.IsHttpOnly).ToArray();
        if (csrf.Length > 1) return null;
        return new LocalWalletSession(pair.Single(c => c.Name == "techstack_session").Value,
            pair.Single(c => c.Name == "techstack_session_origin").Value, csrf.SingleOrDefault()?.Value ?? "");
    }

    private async void OnWalletRevealRequested(object? sender, CoreWebView2WebMessageReceivedEventArgs args)
    {
        var source = args.Source;
        var revision = _deviceContextDocumentRevision;
        var token = _deviceContextDocumentToken;
        if (!IsLocalWalletDocument(source, revision, token) || args.WebMessageAsJson.Length > 4096) return;
        WalletRevealRequest? request;
        try { request = JsonSerializer.Deserialize<WalletRevealRequest>(args.WebMessageAsJson, DeviceContextJson); }
        catch (JsonException) { return; }
        if (request?.Type == "kombify.techstack.wallet.cancel.v1" && request.RequestId == _walletRevealRequestId
            && request.DocumentRevision == revision && request.DocumentToken == token)
        { _walletRevealCancellation?.Cancel(); return; }
        if (request?.Type != "kombify.techstack.wallet.reveal.v1" || !Guid.TryParse(request.RequestId, out _)
            || !LocalWalletRevealProof.ValidItemId(request.ItemId) || request.Reason is null || request.Reason.Length > 1024
            || request.DocumentRevision != revision || request.DocumentToken != token || _walletRevealCancellation is not null) return;
        using var cancellation = new CancellationTokenSource();
        _walletRevealCancellation = cancellation;
        _walletRevealRequestId = request.RequestId;
        LocalWalletResult? result = null;
        var status = "failed";
        try
        {
            var session = await ReadWalletSession(source, revision, token, cancellation.Token);
            if (!IsLocalWalletDocument(source, revision, token)) return;
            if (session is null) throw new InvalidOperationException(LocalRuntimeMessages.WalletRevealFailed);
            var secret = ReadLocalDeviceToken();
            using var http = new HttpClient(new HttpClientHandler { AllowAutoRedirect = false, UseCookies = false });
            result = await LocalWalletRevealProof.RevealAsync(http, _config.LocalUiUri()!, secret, session,
                request.ItemId, request.Reason, (challenge, ct) =>
                {
                    if (ct.IsCancellationRequested || !IsLocalWalletDocument(source, revision, token)) return Task.FromResult(false);
                    var confirmed = MessageBox.Show(this, string.Format(LocalRuntimeMessages.WalletRevealPrompt, challenge.DisplayName),
                        LocalRuntimeMessages.WalletRevealTitle, MessageBoxButtons.OKCancel, MessageBoxIcon.Warning,
                        MessageBoxDefaultButton.Button2) == DialogResult.OK;
                    return Task.FromResult(confirmed && !ct.IsCancellationRequested && IsLocalWalletDocument(source, revision, token));
                }, ct => ReadWalletSession(source, revision, token, ct), cancellation.Token);
            status = result is null ? "cancelled" : "revealed";
            if (result is not null && (await ReadWalletSession(source, revision, token, cancellation.Token))?.Digest != session.Digest)
            { result = null; status = "failed"; }
        }
        catch (OperationCanceledException) { status = "cancelled"; result = null; }
        catch (Exception error) when (error is InvalidOperationException or HttpRequestException or JsonException or FormatException or System.Runtime.InteropServices.COMException)
        { status = "failed"; result = null; }
        finally { _walletRevealCancellation = null; _walletRevealRequestId = null; }
        if (!IsLocalWalletDocument(source, revision, token)) return;
        _webView.CoreWebView2.PostWebMessageAsJson(JsonSerializer.Serialize(new
        {
            type = "kombify.techstack.wallet.reveal.result.v1", request_id = request.RequestId,
            document_revision = revision, document_token = token, status, result,
            error = status == "failed" ? LocalRuntimeMessages.WalletRevealFailed : null
        }, DeviceContextJson));
    }
}
