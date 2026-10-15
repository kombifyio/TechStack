using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Web.WebView2.Core;

namespace Kombify.TechStack.Client;

internal sealed partial class ClientWindow
{
    private const string DeviceContextRead = "kombify.techstack.device-context.read.v1";
    private const string DeviceContextResult = "kombify.techstack.device-context.result.v1";
    private long _deviceContextDocumentRevision;
    private bool _deviceContextNavigating;
    private ulong _deviceContextNavigationId;
    private string _deviceContextDocumentToken = "";

    private sealed record DeviceContextRequest(
        string Type,
        string RequestId,
        long DocumentRevision,
        string DocumentToken);

    private static readonly JsonSerializerOptions DeviceContextJson = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow
    };

    private void RegisterDeviceContextBridge()
    {
        _webView.CoreWebView2.NavigationStarting += (_, args) =>
        {
            if (args.Cancel) return;
            _deviceContextDocumentRevision++;
            _deviceContextNavigating = true;
            _deviceContextNavigationId = args.NavigationId;
            _deviceContextDocumentToken = Guid.NewGuid().ToString();
        };
        _webView.CoreWebView2.NavigationCompleted += async (_, args) =>
        {
            if (args.NavigationId != _deviceContextNavigationId) return;
            _deviceContextNavigating = false;
            var source = _webView.CoreWebView2.Source;
            var revision = _deviceContextDocumentRevision;
            var token = _deviceContextDocumentToken;
            if (!args.IsSuccess || !IsCurrentDeviceContextDocument(source, revision, token)) return;
            var binding = JsonSerializer.Serialize(new { document_revision = revision, document_token = token });
            try
            {
                // The token binds reads to this document, even across same-URL reloads.
                await _webView.CoreWebView2.ExecuteScriptAsync($"window.__kombifyTechstackDocument = {binding}; window.dispatchEvent(new Event('kombify-techstack-device-context-ready'));");
            }
            catch (InvalidOperationException) { }
            catch (System.Runtime.InteropServices.COMException) { }
        };
        _webView.CoreWebView2.WebMessageReceived += OnDeviceContextRequested;
        RegisterWalletRevealBridge();
    }

    private bool IsCurrentDeviceContextDocument(string source, long revision, string token)
    {
        if (IsDisposed) return false;
        var current = _webView.CoreWebView2.Source;
        var authority = _boundServerProfile?.BaseUrl ?? _config.InitialUrl();
        return ClientAuthority.AllowsDeviceContextReply(source, current, authority,
            revision, _deviceContextDocumentRevision, token, _deviceContextDocumentToken, _deviceContextNavigating);
    }

    private void OnDeviceContextRequested(object? sender, CoreWebView2WebMessageReceivedEventArgs args)
    {
        var revision = _deviceContextDocumentRevision;
        var token = _deviceContextDocumentToken;
        if (!IsCurrentDeviceContextDocument(args.Source, revision, token)) return;
        DeviceContextRequest? request;
        try
        {
            if (args.WebMessageAsJson.Length > 1024) return;
            request = JsonSerializer.Deserialize<DeviceContextRequest>(args.WebMessageAsJson, DeviceContextJson);
        }
        catch (JsonException) { return; }
        if (request?.Type != DeviceContextRead || !Guid.TryParse(request.RequestId, out _)
            || request.DocumentRevision != revision || request.DocumentToken != token) return;

        var source = args.Source;
        // Recheck after queued navigation events, before delivering to the document.
        BeginInvoke(new Action(() =>
        {
            if (!IsCurrentDeviceContextDocument(source, revision, token)) return;
            var selected = new Uri(_boundServerProfile?.BaseUrl ?? _config.InitialUrl());
            _webView.CoreWebView2.PostWebMessageAsJson(JsonSerializer.Serialize(new
            {
                type = DeviceContextResult,
                request_id = request.RequestId,
                document_revision = revision,
                context = new
                {
                    installer_edition = DesktopEdition.Name,
                    connection_mode = _config.Mode,
                    base_url = selected.GetLeftPart(UriPartial.Authority),
                    instance_id = _boundServerProfile?.InstanceId
                }
            }));
        }));
    }
}
