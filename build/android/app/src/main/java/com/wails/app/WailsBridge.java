package com.wails.app;

import android.content.Context;
import android.util.Log;
import android.webkit.WebView;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * WailsBridge manages the connection between the Java/Android side and the Go native library.
 * It handles:
 * - Loading and initializing the native Go library
 * - Serving asset requests from Go
 * - Passing messages between JavaScript and Go
 * - Managing callbacks for async operations
 */
public class WailsBridge {
    private static final String TAG = "WailsBridge";

    static {
        // Load the native Go library
        System.loadLibrary("wails");
    }

    private final Context context;
    private final AtomicInteger callbackIdGenerator = new AtomicInteger(0);
    private final ConcurrentHashMap<Integer, AssetCallback> pendingAssetCallbacks = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<Integer, MessageCallback> pendingMessageCallbacks = new ConcurrentHashMap<>();
    private WebView webView;
    private volatile boolean initialized = false;

    // Native methods - implemented in Go
    private static native void nativeInit(WailsBridge bridge);
    private static native void nativeShutdown();
    private static native void nativeOnResume();
    private static native void nativeOnPause();
    private static native void nativeOnPageFinished(String url);
    private static native byte[] nativeServeAsset(String path, String method, String headers);
    private static native String nativeHandleMessage(String message);
    private static native String nativeHandleRuntimeCall(String payload);
    private static native String nativeGetAssetMimeType(String path);
    // Delivers an async native result back to Go (and on to JS) as a custom
    // event with a JSON payload. Used by the biometric prompt.
    private static native void nativeEmitEvent(String name, String json);

    public WailsBridge(Context context) {
        this.context = context;
    }

    /**
     * Initialize the native Go library
     */
    public void initialize() {
        if (initialized) {
            return;
        }

        Log.i(TAG, "Initializing Wails bridge...");
        try {
            nativeInit(this);
            initialized = true;
            Log.i(TAG, "Wails bridge initialized successfully");
        } catch (Exception e) {
            Log.e(TAG, "Failed to initialize Wails bridge", e);
        }
    }

    /**
     * Shutdown the native Go library
     */
    public void shutdown() {
        if (!initialized) {
            return;
        }

        Log.i(TAG, "Shutting down Wails bridge...");
        try {
            nativeShutdown();
            initialized = false;
        } catch (Exception e) {
            Log.e(TAG, "Error during shutdown", e);
        }
    }

    /**
     * Called when the activity resumes
     */
    public void onResume() {
        if (initialized) {
            nativeOnResume();
        }
    }

    /**
     * Called when the activity pauses
     */
    public void onPause() {
        if (initialized) {
            nativeOnPause();
        }
    }

    /**
     * Serve an asset from the Go asset server
     * @param path The URL path requested
     * @param method The HTTP method
     * @param headers The request headers as JSON
     * @return The asset data, or null if not found
     */
    public byte[] serveAsset(String path, String method, String headers) {
        if (!initialized) {
            Log.w(TAG, "Bridge not initialized, cannot serve asset: " + path);
            return null;
        }

        Log.d(TAG, "Serving asset: " + path);
        try {
            return nativeServeAsset(path, method, headers);
        } catch (Exception e) {
            Log.e(TAG, "Error serving asset: " + path, e);
            return null;
        }
    }

    /**
     * Get the MIME type for an asset
     * @param path The asset path
     * @return The MIME type string
     */
    public String getAssetMimeType(String path) {
        if (!initialized) {
            return "application/octet-stream";
        }

        try {
            String mimeType = nativeGetAssetMimeType(path);
            return mimeType != null ? mimeType : "application/octet-stream";
        } catch (Exception e) {
            Log.e(TAG, "Error getting MIME type for: " + path, e);
            return "application/octet-stream";
        }
    }

    /**
     * Handle a message from JavaScript
     * @param message The message from JavaScript (JSON)
     * @return The response to send back to JavaScript (JSON)
     */
    public String handleMessage(String message) {
        if (!initialized) {
            Log.w(TAG, "Bridge not initialized, cannot handle message");
            return "{\"error\":\"Bridge not initialized\"}";
        }

        Log.d(TAG, "Handling message from JS: " + message);
        try {
            return nativeHandleMessage(message);
        } catch (Exception e) {
            Log.e(TAG, "Error handling message", e);
            return "{\"error\":\"" + e.getMessage() + "\"}";
        }
    }

    /**
     * Handle a runtime call (a bound-method IPC) from JavaScript. Unlike
     * handleMessage (fire-and-forget window events), this returns the call
     * result as a {"ok":bool,"data"|"text"|"error":...} envelope that the
     * @wailsio/runtime android transport unwraps. Routed to the Go
     * MessageProcessor via nativeHandleRuntimeCall.
     * @param payload JSON {object,method,args,windowName,clientId}
     * @return JSON envelope {"ok":...}
     */
    public String handleRuntimeCall(String payload) {
        if (!initialized) {
            Log.w(TAG, "Bridge not initialized, cannot handle runtime call");
            return "{\"ok\":false,\"error\":\"Bridge not initialized\"}";
        }
        try {
            return nativeHandleRuntimeCall(payload);
        } catch (Exception e) {
            Log.e(TAG, "Error handling runtime call", e);
            return "{\"ok\":false,\"error\":\"" + e.getMessage() + "\"}";
        }
    }

    // ----- Mobile-feature bridge methods. The Wails native layer invokes
    // these by exact name via JNI (see mobile_features_android.go). -----

    // A ssh-tool:// deep link captured before the WebView/frontend was ready
    // (cold launch via a link). The frontend drains it via
    // WailsJSBridge.takeDeepLink() on startup; if the page is already up when
    // a link arrives (onNewIntent), MainActivity also nudges the frontend to
    // drain immediately.
    private volatile String pendingDeepLink;

    void setPendingDeepLink(String url) {
        if (url != null && !url.isEmpty()) {
            pendingDeepLink = url;
        }
    }

    String takePendingDeepLink() {
        String v = pendingDeepLink;
        pendingDeepLink = null;
        return v != null ? v : "";
    }

    /** Nudge the frontend to drain a deep link that arrived while it was
     *  already running (warm launch / onNewIntent). */
    void notifyDeepLink() {
        executeJavaScript("window.dispatchEvent(new Event('android-deep-link'));");
    }

    private SecureStore secureStore;

    private SecureStore store() {
        if (secureStore == null) {
            secureStore = new SecureStore(context);
        }
        return secureStore;
    }

    /** secureSet stores a value in Keystore-backed encrypted prefs.
     *  Payload: {"key":...,"value":...} (JSON). */
    public void secureSet(String jsonPayload) {
        try {
            org.json.JSONObject o = new org.json.JSONObject(jsonPayload);
            store().set(o.getString("key"), o.getString("value"));
        } catch (Exception e) {
            Log.e(TAG, "secureSet failed", e);
        }
    }

    /** secureGet returns the stored value for key, or "" if absent. */
    public String secureGet(String key) {
        try {
            String v = store().get(key);
            return v != null ? v : "";
        } catch (Exception e) {
            Log.e(TAG, "secureGet failed", e);
            return "";
        }
    }

    /** secureDelete removes a stored value. */
    public void secureDelete(String key) {
        try {
            store().delete(key);
        } catch (Exception e) {
            Log.e(TAG, "secureDelete failed", e);
        }
    }

    /** authenticate shows the system BiometricPrompt. The outcome is
     *  delivered asynchronously to JS as the "common:biometric" event
     *  {ok:bool, error?:string}. */
    public void authenticate(String reason) {
        if (!(context instanceof androidx.fragment.app.FragmentActivity)) {
            emitBiometric(false, "no activity");
            return;
        }
        androidx.fragment.app.FragmentActivity activity =
                (androidx.fragment.app.FragmentActivity) context;
        activity.runOnUiThread(() -> {
            try {
                java.util.concurrent.Executor exec =
                        androidx.core.content.ContextCompat.getMainExecutor(context);
                androidx.biometric.BiometricPrompt prompt = new androidx.biometric.BiometricPrompt(
                        activity, exec,
                        new androidx.biometric.BiometricPrompt.AuthenticationCallback() {
                            @Override
                            public void onAuthenticationSucceeded(
                                    androidx.biometric.BiometricPrompt.AuthenticationResult result) {
                                emitBiometric(true, null);
                            }
                            @Override
                            public void onAuthenticationError(int errorCode, CharSequence errString) {
                                emitBiometric(false, errString != null ? errString.toString() : "error");
                            }
                            @Override
                            public void onAuthenticationFailed() {
                                // Single mismatch (e.g. wrong fingerprint); the prompt stays
                                // up for a retry, so don't resolve here.
                            }
                        });
                androidx.biometric.BiometricPrompt.PromptInfo info =
                        new androidx.biometric.BiometricPrompt.PromptInfo.Builder()
                                .setTitle("Unlock ssh-tool")
                                .setSubtitle(reason != null && !reason.isEmpty() ? reason : "Unlock your vault")
                                .setNegativeButtonText("Use passphrase")
                                .setAllowedAuthenticators(
                                        androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
                                        | androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_WEAK)
                                .build();
                prompt.authenticate(info);
            } catch (Exception e) {
                Log.e(TAG, "authenticate failed", e);
                emitBiometric(false, e.getMessage());
            }
        });
    }

    /** startForegroundService keeps the process alive (with an ongoing
     *  notification) while SSH sessions are connected. Payload JSON:
     *  {"title","text"}. Called by the Go side on first connect. */
    public void startForegroundService(String jsonPayload) {
        try {
            String title = "ssh-tool";
            String text = "SSH sessions running";
            if (jsonPayload != null && !jsonPayload.isEmpty()) {
                org.json.JSONObject o = new org.json.JSONObject(jsonPayload);
                title = o.optString("title", title);
                text = o.optString("text", text);
            }
            SessionService.start(context, title, text);
        } catch (Exception e) {
            Log.e(TAG, "startForegroundService failed", e);
        }
    }

    /** openURL opens a URL in the system browser via Intent.ACTION_VIEW.
     *  Invoked by the Go side (application.AndroidOpenURL) for the opkssh
     *  OIDC login flow, which on android cannot shell out to xdg-open. */
    public void openURL(String url) {
        if (url == null || url.isEmpty()) {
            return;
        }
        try {
            android.content.Intent intent = new android.content.Intent(
                    android.content.Intent.ACTION_VIEW, android.net.Uri.parse(url));
            // Launching from a non-Activity context (the bridge holds the
            // app context) requires NEW_TASK.
            intent.addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK);
            context.startActivity(intent);
        } catch (Exception e) {
            Log.e(TAG, "openURL failed: " + url, e);
        }
    }

    /** stopForegroundService stops the keep-alive service (last disconnect). */
    public void stopForegroundService() {
        try {
            SessionService.stop(context);
        } catch (Exception e) {
            Log.e(TAG, "stopForegroundService failed", e);
        }
    }

    private void emitBiometric(boolean ok, String error) {
        String json = error != null
                ? "{\"ok\":false,\"error\":\"" + escapeJsString(error) + "\"}"
                : "{\"ok\":true}";
        try {
            nativeEmitEvent("common:biometric", json);
        } catch (Exception e) {
            Log.e(TAG, "emit common:biometric failed", e);
        }
    }

    /**
     * Inject the Wails runtime JavaScript into the WebView.
     * Called when the page finishes loading.
     * @param webView The WebView to inject into
     * @param url The URL that finished loading
     */
    public void injectRuntime(WebView webView, String url) {
        this.webView = webView;
        // Notify Go side that page has finished loading so it can inject the runtime
        Log.d(TAG, "Page finished loading: " + url + ", notifying Go side");
        if (initialized) {
            nativeOnPageFinished(url);
        }
    }

    /**
     * Execute JavaScript in the WebView (called from Go side)
     * @param js The JavaScript code to execute
     */
    public void executeJavaScript(String js) {
        if (webView != null) {
            webView.post(() -> webView.evaluateJavascript(js, null));
        }
    }

    /**
     * Called from Go when an event needs to be emitted to JavaScript
     * @param eventName The event name
     * @param eventData The event data (JSON)
     */
    public void emitEvent(String eventName, String eventData) {
        String js = String.format("window.wails && window.wails._emit('%s', %s);",
                escapeJsString(eventName), eventData);
        executeJavaScript(js);
    }

    private String escapeJsString(String str) {
        return str.replace("\\", "\\\\")
                .replace("'", "\\'")
                .replace("\n", "\\n")
                .replace("\r", "\\r");
    }

    // Callback interfaces
    public interface AssetCallback {
        void onAssetReady(byte[] data, String mimeType);
        void onAssetError(String error);
    }

    public interface MessageCallback {
        void onResponse(String response);
        void onError(String error);
    }
}
