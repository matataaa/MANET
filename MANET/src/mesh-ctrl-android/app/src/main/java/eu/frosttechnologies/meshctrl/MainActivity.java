package eu.frosttechnologies.meshctrl;

import android.Manifest;
import android.annotation.SuppressLint;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.net.http.SslError;
import android.os.Build;
import android.os.Bundle;
import android.os.Environment;
import android.provider.MediaStore;
import android.view.View;
import android.webkit.*;
import android.widget.ProgressBar;
import android.widget.TextView;

import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.core.content.FileProvider;

import java.io.File;
import java.io.IOException;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import androidx.appcompat.app.AppCompatActivity;
import androidx.core.app.NotificationCompat;
import androidx.core.content.ContextCompat;

@SuppressLint("SetJavaScriptEnabled")
public class MainActivity extends AppCompatActivity {

    private static final String PREFS = "mesh_ctrl";
    private static final String KEY_NODE = "node_url";
    private static final String DEFAULT_NODE = "https://radio.mesh";
    private static final String CHANNEL_MESH = "mesh_notifications";
    private static final String CHANNEL_SYSTEM = "mesh_system";

    private WebView webView;
    private ProgressBar progress;
    private TextView errorView;
    private int notifId = 1000;
    private ValueCallback<Uri[]> fileUploadCallback;
    private Uri cameraImageUri;

    private final ActivityResultLauncher<String> notifPermission =
            registerForActivityResult(new ActivityResultContracts.RequestPermission(), granted -> {});

    private final ActivityResultLauncher<String> cameraPermission =
            registerForActivityResult(new ActivityResultContracts.RequestPermission(), granted -> {});

    private final ActivityResultLauncher<Intent> fileChooserLauncher =
            registerForActivityResult(new ActivityResultContracts.StartActivityForResult(), result -> {
                if (fileUploadCallback == null) return;
                Uri[] results = null;
                if (result.getResultCode() == RESULT_OK && result.getData() != null) {
                    String dataString = result.getData().getDataString();
                    if (dataString != null) {
                        results = new Uri[]{Uri.parse(dataString)};
                    }
                } else if (result.getResultCode() == RESULT_OK && cameraImageUri != null) {
                    results = new Uri[]{cameraImageUri};
                }
                fileUploadCallback.onReceiveValue(results);
                fileUploadCallback = null;
            });

    private final ActivityResultLauncher<Intent> nodePickerLauncher =
            registerForActivityResult(new ActivityResultContracts.StartActivityForResult(), result -> {
                if (result.getResultCode() == RESULT_OK && result.getData() != null) {
                    String url = result.getData().getStringExtra("node_url");
                    if (url != null && !url.isEmpty()) {
                        getPrefs().edit().putString(KEY_NODE, url).apply();
                        loadNode();
                    }
                }
            });

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);

        webView = findViewById(R.id.webview);
        progress = findViewById(R.id.progress);
        errorView = findViewById(R.id.error_view);

        createNotificationChannels();
        requestNotificationPermission();
        requestCameraPermission();
        setupWebView();

        findViewById(R.id.btn_settings).setOnClickListener(v -> showLaunchModeDialog());

        findViewById(R.id.btn_eud).setOnClickListener(v -> {
            startActivity(new Intent(this, EudActivity.class));
        });

        findViewById(R.id.btn_node).setOnClickListener(v -> {
            Intent intent = new Intent(this, NodePickerActivity.class);
            intent.putExtra("current_url", getNodeUrl());
            nodePickerLauncher.launch(intent);
        });

        findViewById(R.id.btn_reload).setOnClickListener(v -> loadNode());

        loadNode();
    }

    private void createNotificationChannels() {
        NotificationManager nm = getSystemService(NotificationManager.class);
        nm.createNotificationChannel(new NotificationChannel(
                CHANNEL_MESH, "Mesh Alerts",
                NotificationManager.IMPORTANCE_DEFAULT));
        nm.createNotificationChannel(new NotificationChannel(
                CHANNEL_SYSTEM, "System Notifications",
                NotificationManager.IMPORTANCE_HIGH));
    }

    private void requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 &&
                ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
                        != PackageManager.PERMISSION_GRANTED) {
            notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS);
        }
    }

    private void requestCameraPermission() {
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA)
                != PackageManager.PERMISSION_GRANTED) {
            cameraPermission.launch(Manifest.permission.CAMERA);
        }
    }

    private Uri createImageUri() {
        try {
            String ts = new SimpleDateFormat("yyyyMMdd_HHmmss", Locale.US).format(new Date());
            File dir = getExternalFilesDir(Environment.DIRECTORY_PICTURES);
            File img = File.createTempFile("MESH_" + ts + "_", ".jpg", dir);
            return FileProvider.getUriForFile(this,
                    getPackageName() + ".fileprovider", img);
        } catch (IOException e) {
            return null;
        }
    }

    private void setupWebView() {
        WebSettings ws = webView.getSettings();
        ws.setJavaScriptEnabled(true);
        ws.setDomStorageEnabled(true);
        ws.setMediaPlaybackRequiresUserGesture(false);
        ws.setMixedContentMode(WebSettings.MIXED_CONTENT_ALWAYS_ALLOW);
        ws.setUserAgentString(ws.getUserAgentString() + " MeshCtrl/1.0");

        webView.addJavascriptInterface(new MeshBridge(), "MeshCtrlBridge");

        webView.setWebViewClient(new WebViewClient() {
            @Override
            public void onPageFinished(WebView view, String url) {
                progress.setVisibility(View.GONE);
                errorView.setVisibility(View.GONE);
                webView.setVisibility(View.VISIBLE);
                injectNotificationBridge();
            }

            @Override
            public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
                if (request.isForMainFrame()) {
                    showError("Cannot reach node: " + error.getDescription());
                }
            }

            @Override
            public void onReceivedSslError(WebView view, SslErrorHandler handler, SslError error) {
                handler.proceed();
            }
        });

        webView.setWebChromeClient(new WebChromeClient() {
            @Override
            public void onProgressChanged(WebView view, int newProgress) {
                progress.setProgress(newProgress);
            }

            @Override
            public void onPermissionRequest(PermissionRequest request) {
                request.grant(request.getResources());
            }

            @Override
            public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback,
                                             FileChooserParams params) {
                if (fileUploadCallback != null) {
                    fileUploadCallback.onReceiveValue(null);
                }
                fileUploadCallback = callback;

                Intent cameraIntent = new Intent(MediaStore.ACTION_IMAGE_CAPTURE);
                cameraImageUri = createImageUri();
                if (cameraImageUri != null) {
                    cameraIntent.putExtra(MediaStore.EXTRA_OUTPUT, cameraImageUri);
                }

                Intent fileIntent = new Intent(Intent.ACTION_GET_CONTENT);
                fileIntent.addCategory(Intent.CATEGORY_OPENABLE);
                String[] accept = params.getAcceptTypes();
                if (accept != null && accept.length > 0 && accept[0] != null && !accept[0].isEmpty()) {
                    fileIntent.setType(accept[0]);
                } else {
                    fileIntent.setType("*/*");
                }

                Intent chooser = Intent.createChooser(fileIntent, "Pick file");
                if (cameraImageUri != null) {
                    chooser.putExtra(Intent.EXTRA_INITIAL_INTENTS, new Intent[]{cameraIntent});
                }
                fileChooserLauncher.launch(chooser);
                return true;
            }
        });
    }

    private void injectNotificationBridge() {
        String js = "(function() {" +
                "if (window._meshBridgeInstalled) return;" +
                "window._meshBridgeInstalled = true;" +
                "var origNotify = window.notify;" +
                "window.notify = function(title, body, opts) {" +
                "  var type = (opts && opts.type) || 'info';" +
                "  try { MeshCtrlBridge.onNotification(title || '', body || '', type); } catch(e) {}" +
                "  if (origNotify) origNotify.apply(this, arguments);" +
                "};" +
                "if (window.parent && window.parent !== window) {" +
                "  window.parent.notify = window.notify;" +
                "}" +
                "})();";
        webView.evaluateJavascript(js, null);
    }

    private void loadNode() {
        String url = getNodeUrl();
        progress.setVisibility(View.VISIBLE);
        progress.setProgress(0);
        errorView.setVisibility(View.GONE);
        webView.setVisibility(View.VISIBLE);
        webView.loadUrl(url);
    }

    private void showError(String msg) {
        progress.setVisibility(View.GONE);
        webView.setVisibility(View.GONE);
        errorView.setVisibility(View.VISIBLE);
        errorView.setText(msg + "\n\nTap reload or switch node.");
    }

    private String getNodeUrl() {
        return getPrefs().getString(KEY_NODE, DEFAULT_NODE);
    }

    private SharedPreferences getPrefs() {
        return getSharedPreferences(PREFS, MODE_PRIVATE);
    }

    @Override
    public void onBackPressed() {
        if (webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    private void showLaunchModeDialog() {
        String[] labels = {"Ask every time", "KDU (tactical)", "Management"};
        String[] values = {"ask", "kdu", "mgmt"};
        String current = getPrefs().getString("launch_mode", "ask");
        int checked = 0;
        for (int i = 0; i < values.length; i++) {
            if (values[i].equals(current)) { checked = i; break; }
        }
        new AlertDialog.Builder(this, android.R.style.Theme_DeviceDefault_Dialog)
                .setTitle("Launch mode")
                .setSingleChoiceItems(labels, checked, (dialog, which) -> {
                    getPrefs().edit().putString("launch_mode", values[which]).apply();
                    dialog.dismiss();
                })
                .setNegativeButton("Cancel", null)
                .show();
    }

    private class MeshBridge {
        @JavascriptInterface
        public void onNotification(String title, String body, String type) {
            String channel = "error".equals(type) || "warning".equals(type)
                    ? CHANNEL_SYSTEM : CHANNEL_MESH;
            int icon = "error".equals(type) ? android.R.drawable.ic_dialog_alert
                    : android.R.drawable.ic_dialog_info;

            NotificationCompat.Builder builder = new NotificationCompat.Builder(MainActivity.this, channel)
                    .setSmallIcon(icon)
                    .setContentTitle(title)
                    .setContentText(body)
                    .setAutoCancel(true);

            if ("error".equals(type)) {
                builder.setPriority(NotificationCompat.PRIORITY_HIGH);
            }

            NotificationManager nm = getSystemService(NotificationManager.class);
            nm.notify(notifId++, builder.build());
        }

        @JavascriptInterface
        public String getNodeUrl() {
            return MainActivity.this.getNodeUrl();
        }
    }
}
