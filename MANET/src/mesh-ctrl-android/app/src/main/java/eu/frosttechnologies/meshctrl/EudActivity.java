package eu.frosttechnologies.meshctrl;

import android.annotation.SuppressLint;
import android.content.Intent;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.OpenableColumns;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.KeyEvent;
import android.view.MotionEvent;
import android.view.View;
import android.view.WindowInsets;
import android.view.WindowInsetsController;
import android.view.WindowManager;
import android.view.inputmethod.EditorInfo;
import android.widget.Button;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.appcompat.app.ActionBar;
import androidx.appcompat.app.AppCompatActivity;
import androidx.core.content.FileProvider;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Date;
import java.util.HashSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.TimeZone;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManager;
import javax.net.ssl.X509TrustManager;

@SuppressLint({"SetTextI18n", "ClickableViewAccessibility"})
public class EudActivity extends AppCompatActivity {

    private static final int POLL_MS = 3000;
    private static final int MAX_CHANNELS = 21;
    private static final int VOL_STEP = 10;
    private static final long HOLD_MS = 3000;
    private static final long DEBOUNCE_MS = 4000;
    private static final String[] PAGES = {"RADIO", "MESH", "COMMS", "STATUS", "CHAT"};
    private static final String[] TEMPLATES = {"CHECK IN", "ROGER", "MOVING", "CONTACT"};

    private boolean nvgMode = false;
    private int cPri, cDim, cBg, cBgAct, cMuted;

    private int page = 0;
    private String nodeUrl;
    private boolean polling = false;

    private JSONObject apiData;
    private JSONObject apiLocal;
    private JSONObject apiVoice;
    private JSONObject apiChannels;
    private int chatUnread = 0;
    private boolean chatAvailable = false;
    private JSONArray chatMessages;
    private String chatHostname = "";
    private boolean connected = false;
    private boolean wasConnected = false;
    private boolean pttConnected = false;
    private int lastSeenMsgCount = 0;

    private int txChannel = 1;
    private Set<Integer> rxChannels = new HashSet<>();
    private int micVolume = 80;
    private int spkVolume = 80;

    private long lastUserAction = 0;
    private long lastPollMs = 0;
    private String nodeHostname = "";
    private List<String> chatPeers = new ArrayList<>();
    private int dmTargetIdx = -1;
    private Uri cameraImageUri;

    private TextView tvTitle, tvConn, tvDisplay, tvPages, tvTime;
    private LinearLayout voiceRow, channelGrid, chatInputRow;
    private LinearLayout sidePanel;
    private LinearLayout chatActionRow;
    private LinearLayout templateRow;
    private ScrollView channelScroll;
    private Button dmTargetBtn;
    private FrameLayout topoContainer;
    private MeshTopoView topoView;
    private BftView bftView;
    private EditText chatInput;
    private Button chatSendBtn;
    private Button pttBtn;
    private Button alertBtn;
    private FrameLayout offlineOverlay;
    private TextView offlineSubText;
    private Button[] navButtons;
    private Button[] chanTxButtons;
    private Button[] chanRxButtons;
    private Button homeBtn;
    private long alertDownTime = 0;

    private long homeDownTime = 0;

    private final Handler handler = new Handler(Looper.getMainLooper());
    private final ExecutorService executor = Executors.newSingleThreadExecutor();

    private final ActivityResultLauncher<String> filePickerLauncher =
            registerForActivityResult(new ActivityResultContracts.GetContent(), uri -> {
                if (uri != null) uploadFileFromUri(uri);
            });

    private final ActivityResultLauncher<Uri> cameraLauncher =
            registerForActivityResult(new ActivityResultContracts.TakePicture(), success -> {
                if (success && cameraImageUri != null) uploadFileFromUri(cameraImageUri);
            });

    private final ActivityResultLauncher<String> cameraPermLauncher =
            registerForActivityResult(new ActivityResultContracts.RequestPermission(), granted -> {
                if (granted) doLaunchCamera();
            });

    private final Runnable pollRunnable = new Runnable() {
        @Override
        public void run() {
            if (!polling) return;
            pollData();
            handler.postDelayed(this, POLL_MS);
        }
    };

    private final Runnable homeTickRunnable = new Runnable() {
        @Override
        public void run() {
            if (homeDownTime == 0) return;
            long elapsed = System.currentTimeMillis() - homeDownTime;
            if (elapsed >= HOLD_MS) {
                resetHomeButton();
                startActivity(new Intent(EudActivity.this, MainActivity.class));
                finish();
                return;
            }
            int secsLeft = (int) Math.ceil((HOLD_MS - elapsed) / 1000.0);
            homeBtn.setText("(" + secsLeft + ") MGMT");
            handler.postDelayed(this, 100);
        }
    };

    private void applyColors() {
        if (nvgMode) {
            cPri = 0xFFCC3333; cDim = 0xFF5a1a1a; cBg = 0xFF1a0a0a;
            cBgAct = 0xFF2a0a0a; cMuted = 0xFF8a1a1a;
        } else {
            cPri = 0xFF33FF33; cDim = 0xFF1a5a1a; cBg = 0xFF0a1a0a;
            cBgAct = 0xFF0a2a0a; cMuted = 0xFF1a8a1a;
        }
    }

    private void toggleNvg() {
        nvgMode = !nvgMode;
        applyColors();
        getSharedPreferences("mesh_ctrl", MODE_PRIVATE).edit()
                .putBoolean("nvg_mode", nvgMode).apply();
        tvTitle.setTextColor(cPri);
        tvTitle.setShadowLayer(6, 0, 0, cPri);
        tvDisplay.setTextColor(cPri);
        tvDisplay.setShadowLayer(3, 0, 0, cPri);
        tvPages.setTextColor(cPri);
        tvPages.setShadowLayer(4, 0, 0, cPri);
        tvTime.setTextColor(cMuted);
        chatInput.setTextColor(cPri);
        chatInput.setTextColor(cPri);
        chatInput.setHintTextColor(cDim);
        chatSendBtn.setTextColor(cPri);
        recolorButtonRow(chatActionRow);
        recolorButtonRow(templateRow);
        int[] vIds = {R.id.eud_btn_mic_up, R.id.eud_btn_mic_down, R.id.eud_btn_vol_up, R.id.eud_btn_vol_down};
        for (int id : vIds) {
            Button b = findViewById(id);
            if (b != null) b.setTextColor(cPri);
        }
        if (topoView != null) topoView.setNvgMode(nvgMode);
        if (bftView != null) bftView.setNvgMode(nvgMode);
        if (!pttBtn.getText().equals("TX!")) {
            pttBtn.setTextColor(cPri);
            GradientDrawable pbg = new GradientDrawable();
            pbg.setCornerRadius(dpToPx(4));
            pbg.setColor(cBg);
            pbg.setStroke(dpToPx(2), cPri);
            pttBtn.setBackground(pbg);
        }
        updateDisplay();
    }

    private void recolorButtonRow(LinearLayout row) {
        if (row == null) return;
        for (int i = 0; i < row.getChildCount(); i++) {
            View child = row.getChildAt(i);
            if (child instanceof Button) {
                Button b = (Button) child;
                b.setTextColor(cPri);
                GradientDrawable bg = new GradientDrawable();
                bg.setCornerRadius(dpToPx(3));
                bg.setColor(cBg);
                bg.setStroke(dpToPx(1), cDim);
                b.setBackground(bg);
            }
        }
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        setContentView(R.layout.activity_eud);

        if (savedInstanceState != null) {
            page = savedInstanceState.getInt("page", 0);
        }

        SharedPreferences prefs = getSharedPreferences("mesh_ctrl", MODE_PRIVATE);
        nvgMode = prefs.getBoolean("nvg_mode", false);
        applyColors();

        hideSystemUI();

        ActionBar ab = getSupportActionBar();
        if (ab != null) ab.hide();

        nodeUrl = prefs.getString("node_url", "https://radio.mesh");

        tvTitle = findViewById(R.id.eud_title);
        tvConn = findViewById(R.id.eud_conn);
        tvDisplay = findViewById(R.id.eud_display);
        tvPages = findViewById(R.id.eud_pages);
        tvTime = findViewById(R.id.eud_time);

        tvTitle.setTextColor(cPri);
        tvTitle.setShadowLayer(6, 0, 0, cPri);
        tvDisplay.setTextColor(cPri);
        tvDisplay.setShadowLayer(3, 0, 0, cPri);
        tvPages.setTextColor(cPri);
        tvPages.setShadowLayer(4, 0, 0, cPri);
        tvTime.setTextColor(cMuted);

        tvConn.setOnClickListener(v -> toggleNvg());

        voiceRow = findViewById(R.id.eud_voice_row);
        channelGrid = findViewById(R.id.eud_channel_grid);
        if (channelGrid.getParent() instanceof ScrollView) {
            channelScroll = (ScrollView) channelGrid.getParent();
        }
        chatInputRow = findViewById(R.id.eud_chat_input_row);
        sidePanel = findViewById(R.id.eud_side_panel);
        topoContainer = findViewById(R.id.eud_topo_container);
        if (topoContainer != null) {
            topoView = new MeshTopoView(this);
            topoView.setNvgMode(nvgMode);
            topoView.setOnNodeTapListener((hostname, ip, isMe) -> {
                if (isMe || ip.isEmpty()) {
                    Toast.makeText(this, hostname + (isMe ? " (SELF)" : " NO IP"), Toast.LENGTH_SHORT).show();
                    return;
                }
                Toast.makeText(this, "PING " + hostname + "...", Toast.LENGTH_SHORT).show();
                doPing(hostname, ip);
            });
            topoContainer.addView(topoView);

            bftView = new BftView(this);
            bftView.setNvgMode(nvgMode);
            bftView.setVisibility(View.GONE);
            topoContainer.addView(bftView);
        }
        chatInput = findViewById(R.id.eud_chat_input);
        chatInput.setTextColor(cPri);
        chatInput.setHintTextColor(cDim);
        chatSendBtn = findViewById(R.id.eud_chat_send);
        chatSendBtn.setBackgroundTintList(null);
        chatSendBtn.setStateListAnimator(null);
        chatSendBtn.setTextColor(cPri);
        chatSendBtn.setOnClickListener(v -> sendChatMessage());
        chatInput.setOnEditorActionListener((v, actionId, event) -> {
            if (actionId == EditorInfo.IME_ACTION_SEND) { sendChatMessage(); return true; }
            return false;
        });

        buildChatActionRow();
        buildTemplateRow();
        buildPttButton();

        int[] voiceBtnIds = {R.id.eud_btn_mic_up, R.id.eud_btn_mic_down, R.id.eud_btn_vol_up, R.id.eud_btn_vol_down};
        for (int id : voiceBtnIds) {
            Button b = findViewById(id);
            b.setBackgroundTintList(null);
            b.setStateListAnimator(null);
            b.setTextColor(cPri);
        }
        findViewById(R.id.eud_btn_mic_up).setOnClickListener(v -> adjustRadioVolume("mic", VOL_STEP));
        findViewById(R.id.eud_btn_mic_down).setOnClickListener(v -> adjustRadioVolume("mic", -VOL_STEP));
        findViewById(R.id.eud_btn_vol_up).setOnClickListener(v -> adjustRadioVolume("spk", VOL_STEP));
        findViewById(R.id.eud_btn_vol_down).setOnClickListener(v -> adjustRadioVolume("spk", -VOL_STEP));

        navButtons = new Button[]{
                findViewById(R.id.eud_nav_radio),
                findViewById(R.id.eud_nav_mesh),
                findViewById(R.id.eud_nav_comms),
                findViewById(R.id.eud_nav_stat),
                findViewById(R.id.eud_nav_chat)
        };
        for (Button b : navButtons) { b.setBackgroundTintList(null); b.setStateListAnimator(null); }
        navButtons[0].setOnClickListener(v -> goToPage(0));
        navButtons[1].setOnClickListener(v -> goToPage(1));
        navButtons[2].setOnClickListener(v -> goToPage(2));
        navButtons[3].setOnClickListener(v -> goToPage(3));
        navButtons[4].setOnClickListener(v -> goToPage(4));

        homeBtn = findViewById(R.id.eud_nav_exit);
        homeBtn.setBackgroundTintList(null);
        homeBtn.setStateListAnimator(null);
        setupHomeButton();

        buildChannelGrid();
        buildAlertButton();
        buildOfflineOverlay();
        updateDisplay();
    }

    private void buildChatActionRow() {
        chatActionRow = new LinearLayout(this);
        chatActionRow.setOrientation(LinearLayout.HORIZONTAL);
        chatActionRow.setGravity(Gravity.CENTER);
        chatActionRow.setVisibility(View.GONE);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dpToPx(4);
        chatActionRow.setLayoutParams(lp);

        dmTargetBtn = makeActionButton("ALL");
        dmTargetBtn.setOnClickListener(v -> cycleDmTarget());
        chatActionRow.addView(dmTargetBtn);

        Button fileBtn = makeActionButton("FILE");
        fileBtn.setOnClickListener(v -> { flashButton(fileBtn); filePickerLauncher.launch("*/*"); });
        chatActionRow.addView(fileBtn);

        Button camBtn = makeActionButton("CAM");
        camBtn.setOnClickListener(v -> { flashButton(camBtn); launchCamera(); });
        chatActionRow.addView(camBtn);

        Button syncBtn = makeActionButton("SYNC");
        syncBtn.setOnClickListener(v -> { flashButton(syncBtn); doSync(); });
        chatActionRow.addView(syncBtn);

        if (sidePanel != null) {
            sidePanel.addView(chatActionRow);
        } else {
            LinearLayout root = (LinearLayout) chatInputRow.getParent();
            int idx = root.indexOfChild(chatInputRow);
            root.addView(chatActionRow, idx);
        }
    }

    private void buildTemplateRow() {
        templateRow = new LinearLayout(this);
        templateRow.setOrientation(LinearLayout.HORIZONTAL);
        templateRow.setGravity(Gravity.CENTER);
        templateRow.setVisibility(View.GONE);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        lp.topMargin = dpToPx(2);
        templateRow.setLayoutParams(lp);

        for (String tmpl : TEMPLATES) {
            Button btn = makeActionButton(tmpl);
            btn.setTextSize(TypedValue.COMPLEX_UNIT_SP, 8);
            btn.setOnClickListener(v -> { flashButton(btn); sendTemplate(tmpl); });
            templateRow.addView(btn);
        }

        if (sidePanel != null) {
            sidePanel.addView(templateRow);
        } else {
            LinearLayout root = (LinearLayout) chatInputRow.getParent();
            int idx = root.indexOfChild(chatActionRow);
            root.addView(templateRow, idx);
        }
    }

    private void sendTemplate(String message) {
        String target = getDmTarget();
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String escaped = message.replace("\\", "\\\\").replace("\"", "\\\"");
                String body;
                if (target != null) {
                    String tEsc = target.replace("\\", "\\\\").replace("\"", "\\\"");
                    body = "{\"body\":\"" + escaped + "\",\"to\":[\"" + tEsc + "\"]}";
                } else {
                    body = "{\"body\":\"" + escaped + "\"}";
                }
                postJson(base + "/api/applets/mesh-chat/proxy/send", body);
            } catch (Exception ignored) {}
        });
    }

    private void launchCamera() {
        if (checkSelfPermission(android.Manifest.permission.CAMERA) != android.content.pm.PackageManager.PERMISSION_GRANTED) {
            cameraPermLauncher.launch(android.Manifest.permission.CAMERA);
            return;
        }
        doLaunchCamera();
    }

    private void doLaunchCamera() {
        File photoDir = new File(getExternalFilesDir(null), "Pictures");
        photoDir.mkdirs();
        File photoFile = new File(photoDir, "cam_" + System.currentTimeMillis() + ".jpg");
        cameraImageUri = FileProvider.getUriForFile(this,
                getPackageName() + ".fileprovider", photoFile);
        cameraLauncher.launch(cameraImageUri);
    }

    private void cycleDmTarget() {
        if (chatPeers.isEmpty()) {
            dmTargetIdx = -1;
        } else {
            dmTargetIdx++;
            if (dmTargetIdx >= chatPeers.size()) dmTargetIdx = -1;
        }
        String label = dmTargetIdx < 0 ? "ALL" : truncate(chatPeers.get(dmTargetIdx), 8);
        dmTargetBtn.setText(label);
        flashButton(dmTargetBtn);
    }

    private String getDmTarget() {
        if (dmTargetIdx >= 0 && dmTargetIdx < chatPeers.size()) {
            return chatPeers.get(dmTargetIdx);
        }
        return null;
    }

    private void flashButton(Button btn) {
        GradientDrawable flash = new GradientDrawable();
        flash.setCornerRadius(dpToPx(3));
        flash.setColor(cPri);
        flash.setStroke(dpToPx(1), cPri);
        btn.setBackground(flash);
        btn.setTextColor(0xFF000000);
        handler.postDelayed(() -> {
            GradientDrawable bg = new GradientDrawable();
            bg.setCornerRadius(dpToPx(3));
            bg.setColor(cBg);
            bg.setStroke(dpToPx(1), cDim);
            btn.setBackground(bg);
            btn.setTextColor(cPri);
        }, 150);
    }

    private Button makeActionButton(String text) {
        Button btn = new Button(this);
        btn.setText(text);
        btn.setTextColor(cPri);
        btn.setTextSize(TypedValue.COMPLEX_UNIT_SP, 10);
        btn.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        btn.setAllCaps(false);
        btn.setPadding(0, 0, 0, 0);
        btn.setMinimumWidth(0);
        btn.setMinWidth(0);
        btn.setMinimumHeight(0);
        btn.setMinHeight(0);
        btn.setBackgroundTintList(null);
        btn.setStateListAnimator(null);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(0, dpToPx(32), 1f);
        lp.setMargins(dpToPx(2), 0, dpToPx(2), 0);
        btn.setLayoutParams(lp);
        GradientDrawable bg = new GradientDrawable();
        bg.setCornerRadius(dpToPx(3));
        bg.setColor(cBg);
        bg.setStroke(dpToPx(1), cDim);
        btn.setBackground(bg);
        return btn;
    }

    private void buildPttButton() {
        pttBtn = new Button(this);
        pttBtn.setText("PTT");
        pttBtn.setTextColor(cPri);
        pttBtn.setTextSize(TypedValue.COMPLEX_UNIT_SP, 10);
        pttBtn.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        pttBtn.setAllCaps(false);
        pttBtn.setPadding(0, 0, 0, 0);
        pttBtn.setMinimumWidth(0);
        pttBtn.setMinWidth(0);
        pttBtn.setMinimumHeight(0);
        pttBtn.setMinHeight(0);
        pttBtn.setBackgroundTintList(null);
        pttBtn.setStateListAnimator(null);
        pttBtn.setVisibility(View.GONE);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(0, dpToPx(36), 1f);
        lp.setMargins(dpToPx(3), 0, 0, 0);
        pttBtn.setLayoutParams(lp);
        GradientDrawable bg = new GradientDrawable();
        bg.setCornerRadius(dpToPx(4));
        bg.setColor(cBg);
        bg.setStroke(dpToPx(2), cPri);
        pttBtn.setBackground(bg);

        pttBtn.setOnTouchListener((v, event) -> {
            switch (event.getAction()) {
                case MotionEvent.ACTION_DOWN:
                    setPttState(true);
                    return true;
                case MotionEvent.ACTION_UP:
                case MotionEvent.ACTION_CANCEL:
                    setPttState(false);
                    return true;
            }
            return false;
        });

        voiceRow.addView(pttBtn);
    }

    private void setPttState(boolean active) {
        if (active) {
            GradientDrawable bg = new GradientDrawable();
            bg.setCornerRadius(dpToPx(4));
            bg.setColor(0xFFFF3333);
            bg.setStroke(dpToPx(2), 0xFFFF3333);
            pttBtn.setBackground(bg);
            pttBtn.setTextColor(0xFF000000);
            pttBtn.setText("TX!");
        } else {
            GradientDrawable bg = new GradientDrawable();
            bg.setCornerRadius(dpToPx(4));
            bg.setColor(cBg);
            bg.setStroke(dpToPx(2), cPri);
            pttBtn.setBackground(bg);
            pttBtn.setTextColor(cPri);
            pttBtn.setText("PTT");
        }
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                postJson(base + "/api/voice", "{\"action\":\"" + (active ? "ptt_on" : "ptt_off") + "\"}");
            } catch (Exception ignored) {}
        });
    }

    private final Runnable alertTickRunnable = new Runnable() {
        @Override
        public void run() {
            if (alertDownTime == 0) return;
            long elapsed = System.currentTimeMillis() - alertDownTime;
            if (elapsed >= 2000) {
                alertDownTime = 0;
                resetAlertButton();
                sendDuress();
                return;
            }
            int secsLeft = (int) Math.ceil((2000 - elapsed) / 1000.0);
            alertBtn.setText("(" + secsLeft + ") ALERT");
            handler.postDelayed(this, 100);
        }
    };

    private void buildAlertButton() {
        alertBtn = new Button(this);
        alertBtn.setText("DURESS");
        alertBtn.setTextColor(0xFFFF3333);
        alertBtn.setTextSize(TypedValue.COMPLEX_UNIT_SP, 11);
        alertBtn.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        alertBtn.setAllCaps(false);
        alertBtn.setPadding(0, 0, 0, 0);
        alertBtn.setMinimumWidth(0);
        alertBtn.setMinWidth(0);
        alertBtn.setMinimumHeight(0);
        alertBtn.setMinHeight(0);
        alertBtn.setBackgroundTintList(null);
        alertBtn.setStateListAnimator(null);
        alertBtn.setVisibility(View.GONE);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dpToPx(40));
        lp.topMargin = dpToPx(4);
        alertBtn.setLayoutParams(lp);
        resetAlertButton();

        alertBtn.setOnTouchListener((v, event) -> {
            switch (event.getAction()) {
                case MotionEvent.ACTION_DOWN:
                    alertDownTime = System.currentTimeMillis();
                    alertBtn.setText("(2) ALERT");
                    GradientDrawable press = new GradientDrawable();
                    press.setCornerRadius(dpToPx(4));
                    press.setColor(0xFF330000);
                    press.setStroke(dpToPx(2), 0xFFFF3333);
                    alertBtn.setBackground(press);
                    handler.post(alertTickRunnable);
                    return true;
                case MotionEvent.ACTION_UP:
                case MotionEvent.ACTION_CANCEL:
                    alertDownTime = 0;
                    handler.removeCallbacks(alertTickRunnable);
                    resetAlertButton();
                    return true;
            }
            return false;
        });

        LinearLayout navParent = (LinearLayout) voiceRow.getParent();
        int navIdx = navParent.indexOfChild(voiceRow);
        navParent.addView(alertBtn, navIdx);
    }

    private void resetAlertButton() {
        GradientDrawable bg = new GradientDrawable();
        bg.setCornerRadius(dpToPx(4));
        bg.setColor(0xFF1a0000);
        bg.setStroke(dpToPx(2), 0xFFCC3333);
        alertBtn.setBackground(bg);
        alertBtn.setText("DURESS");
        alertBtn.setTextColor(0xFFFF3333);
    }

    private void sendDuress() {
        Toast.makeText(this, "DURESS SENT", Toast.LENGTH_LONG).show();
        String gps = "NO FIX";
        if (apiLocal != null) {
            JSONObject gpsObj = apiLocal.optJSONObject("gps");
            if (gpsObj != null && gpsObj.optBoolean("connected", false)) {
                String lat = gpsObj.optString("lat", ""), lon = gpsObj.optString("lon", "");
                if (!lat.isEmpty() && !lon.isEmpty()) gps = lat + "," + lon;
            }
        }
        String msg = "!DURESS! " + nodeHostname + " @ " + gps + " - IMMEDIATE ASSISTANCE REQUIRED";
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String escaped = msg.replace("\\", "\\\\").replace("\"", "\\\"");
                postJson(base + "/api/applets/mesh-chat/proxy/send", "{\"body\":\"" + escaped + "\"}");
            } catch (Exception ignored) {}
        });
    }

    private void doSync() {
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                postJson(base + "/api/applets/mesh-chat/proxy/sync", "{}");
            } catch (Exception ignored) {}
        });
    }

    private void doPing(String hostname, String ip) {
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String body = "{\"target\":\"" + ip + "\",\"count\":3,\"interval\":0.5}";
                HttpURLConnection conn = openConnection(base + "/api/ping");
                conn.setRequestMethod("POST");
                conn.setRequestProperty("Content-Type", "application/json");
                conn.setDoOutput(true);
                conn.setConnectTimeout(5000);
                conn.setReadTimeout(10000);
                OutputStream os = conn.getOutputStream();
                os.write(body.getBytes(StandardCharsets.UTF_8));
                os.close();
                BufferedReader reader = new BufferedReader(new InputStreamReader(conn.getInputStream()));
                StringBuilder sb = new StringBuilder();
                String line;
                while ((line = reader.readLine()) != null) sb.append(line);
                reader.close();
                JSONObject resp = new JSONObject(sb.toString());
                JSONObject result = resp.optJSONObject("result");
                String msg;
                if (result != null && result.has("rtt_avg")) {
                    double avg = result.optDouble("rtt_avg", 0);
                    int loss = result.optInt("loss_pct", 0);
                    msg = hostname + ": " + String.format(Locale.US, "%.1fms", avg)
                            + (loss > 0 ? " (" + loss + "% loss)" : "");
                } else {
                    msg = hostname + ": TIMEOUT";
                }
                runOnUiThread(() -> Toast.makeText(this, msg, Toast.LENGTH_LONG).show());
            } catch (Exception e) {
                runOnUiThread(() -> Toast.makeText(this, hostname + ": UNREACHABLE", Toast.LENGTH_SHORT).show());
            }
        });
    }

    private void uploadFileFromUri(Uri uri) {
        executor.execute(() -> {
            try {
                String fileName = "file";
                Cursor cursor = getContentResolver().query(uri, null, null, null, null);
                if (cursor != null) {
                    if (cursor.moveToFirst()) {
                        int idx = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME);
                        if (idx >= 0) fileName = cursor.getString(idx);
                    }
                    cursor.close();
                }

                InputStream is = getContentResolver().openInputStream(uri);
                if (is == null) return;
                ByteArrayOutputStream baos = new ByteArrayOutputStream();
                byte[] buf = new byte[8192];
                int n;
                while ((n = is.read(buf)) != -1) baos.write(buf, 0, n);
                is.close();
                byte[] fileData = baos.toByteArray();

                String boundary = "----EudUpload" + System.nanoTime();
                String base = nodeUrl.replaceAll("/$", "");
                HttpURLConnection conn = openConnection(base + "/api/applets/mesh-chat/proxy/upload");
                conn.setRequestMethod("POST");
                conn.setRequestProperty("Content-Type", "multipart/form-data; boundary=" + boundary);
                conn.setDoOutput(true);
                conn.setConnectTimeout(15000);
                conn.setReadTimeout(15000);

                OutputStream os = conn.getOutputStream();
                String header = "--" + boundary + "\r\n" +
                        "Content-Disposition: form-data; name=\"file\"; filename=\"" + fileName + "\"\r\n" +
                        "Content-Type: application/octet-stream\r\n\r\n";
                os.write(header.getBytes(StandardCharsets.UTF_8));
                os.write(fileData);
                os.write(("\r\n--" + boundary + "--\r\n").getBytes(StandardCharsets.UTF_8));
                os.close();
                conn.getResponseCode();
                conn.disconnect();
            } catch (Exception ignored) {}
        });
    }

    private void setupHomeButton() {
        homeBtn.setOnTouchListener((v, event) -> {
            switch (event.getAction()) {
                case MotionEvent.ACTION_DOWN:
                    homeDownTime = System.currentTimeMillis();
                    homeBtn.setText("(3) MGMT");
                    homeBtn.setTextColor(cPri);
                    homeBtn.setGravity(Gravity.START | Gravity.CENTER_VERTICAL);
                    homeBtn.setPadding(dpToPx(4), 0, 0, 0);
                    handler.post(homeTickRunnable);
                    return true;
                case MotionEvent.ACTION_UP:
                case MotionEvent.ACTION_CANCEL:
                    long held = System.currentTimeMillis() - homeDownTime;
                    resetHomeButton();
                    if (held >= HOLD_MS) {
                        startActivity(new Intent(EudActivity.this, MainActivity.class));
                        finish();
                    } else if (held < 500) {
                        Intent home = new Intent(Intent.ACTION_MAIN);
                        home.addCategory(Intent.CATEGORY_HOME);
                        home.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                        startActivity(home);
                    }
                    return true;
            }
            return false;
        });
    }

    private void resetHomeButton() {
        homeDownTime = 0;
        handler.removeCallbacks(homeTickRunnable);
        homeBtn.setText("HOME");
        homeBtn.setGravity(Gravity.CENTER);
        homeBtn.setPadding(0, 0, 0, 0);
        homeBtn.setTextColor(cPri);
    }

    private void buildOfflineOverlay() {
        offlineOverlay = new FrameLayout(this);
        offlineOverlay.setLayoutParams(new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT));
        offlineOverlay.setBackgroundColor(0xCC0a0f0a);
        offlineOverlay.setClickable(true);

        LinearLayout box = new LinearLayout(this);
        box.setOrientation(LinearLayout.VERTICAL);
        box.setGravity(Gravity.CENTER);
        box.setPadding(dpToPx(24), dpToPx(16), dpToPx(24), dpToPx(16));

        GradientDrawable boxBg = new GradientDrawable();
        boxBg.setCornerRadius(dpToPx(6));
        boxBg.setColor(0xFF1a0808);
        boxBg.setStroke(dpToPx(2), 0xFFCC3333);
        box.setBackground(boxBg);

        FrameLayout.LayoutParams boxLp = new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.WRAP_CONTENT, FrameLayout.LayoutParams.WRAP_CONTENT);
        boxLp.gravity = Gravity.CENTER;
        box.setLayoutParams(boxLp);

        View line = new View(this);
        line.setBackgroundColor(0xFFCC3333);
        LinearLayout.LayoutParams lineLp = new LinearLayout.LayoutParams(dpToPx(120), dpToPx(2));
        lineLp.bottomMargin = dpToPx(8);
        line.setLayoutParams(lineLp);
        box.addView(line);

        TextView title = new TextView(this);
        title.setText("OFFLINE");
        title.setTextColor(0xFFCC3333);
        title.setTextSize(TypedValue.COMPLEX_UNIT_SP, 24);
        title.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        title.setGravity(Gravity.CENTER);
        box.addView(title);

        View line2 = new View(this);
        line2.setBackgroundColor(0xFFCC3333);
        LinearLayout.LayoutParams line2Lp = new LinearLayout.LayoutParams(dpToPx(120), dpToPx(2));
        line2Lp.topMargin = dpToPx(8);
        line2.setLayoutParams(line2Lp);
        box.addView(line2);

        offlineSubText = new TextView(this);
        offlineSubText.setText("RECONNECTING");
        offlineSubText.setTextColor(0xFF993333);
        offlineSubText.setTextSize(TypedValue.COMPLEX_UNIT_SP, 12);
        offlineSubText.setTypeface(Typeface.MONOSPACE);
        offlineSubText.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams subLp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT);
        subLp.topMargin = dpToPx(6);
        offlineSubText.setLayoutParams(subLp);
        offlineSubText.setVisibility(View.GONE);
        box.addView(offlineSubText);

        offlineOverlay.addView(box);
        ((FrameLayout) findViewById(android.R.id.content)).addView(offlineOverlay);
    }

    private void buildChannelGrid() {
        chanTxButtons = new Button[MAX_CHANNELS];
        chanRxButtons = new Button[MAX_CHANNELS];
        int cols = 7;
        int rows = 3;
        int dp2 = dpToPx(2);
        int btnH = dpToPx(32);

        for (int r = 0; r < rows; r++) {
            LinearLayout txRow = new LinearLayout(this);
            txRow.setOrientation(LinearLayout.HORIZONTAL);
            txRow.setGravity(Gravity.CENTER);
            LinearLayout.LayoutParams txLp = new LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
            if (r > 0) txLp.topMargin = dpToPx(6);
            txRow.setLayoutParams(txLp);

            LinearLayout rxRow = new LinearLayout(this);
            rxRow.setOrientation(LinearLayout.HORIZONTAL);
            rxRow.setGravity(Gravity.CENTER);
            LinearLayout.LayoutParams rxLp = new LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT);
            rxLp.topMargin = dp2;
            rxRow.setLayoutParams(rxLp);

            for (int c = 0; c < cols; c++) {
                int ch = r * cols + c + 1;
                if (ch > MAX_CHANNELS) break;
                final int channel = ch;
                String label = String.format(Locale.US, "%02d", ch);

                Button txBtn = makeChanButton(label + "TX");
                LinearLayout.LayoutParams tlp = new LinearLayout.LayoutParams(0, btnH, 1f);
                tlp.setMargins(c > 0 ? dp2 : 0, 0, 0, 0);
                txBtn.setLayoutParams(tlp);
                txBtn.setOnClickListener(v -> setTxChannel(channel));
                chanTxButtons[ch - 1] = txBtn;
                txRow.addView(txBtn);

                Button rxBtn = makeChanButton(label + "RX");
                LinearLayout.LayoutParams rlp = new LinearLayout.LayoutParams(0, btnH, 1f);
                rlp.setMargins(c > 0 ? dp2 : 0, 0, 0, 0);
                rxBtn.setLayoutParams(rlp);
                rxBtn.setOnClickListener(v -> toggleRxChannel(channel));
                chanRxButtons[ch - 1] = rxBtn;
                rxRow.addView(rxBtn);
            }
            channelGrid.addView(txRow);
            channelGrid.addView(rxRow);
        }
    }

    private Button makeChanButton(String text) {
        Button btn = new Button(this);
        btn.setText(text);
        btn.setTextColor(cPri);
        btn.setTextSize(TypedValue.COMPLEX_UNIT_SP, 10);
        btn.setTypeface(Typeface.MONOSPACE, Typeface.BOLD);
        btn.setAllCaps(false);
        btn.setPadding(0, 0, 0, 0);
        btn.setMinimumWidth(0);
        btn.setMinWidth(0);
        btn.setMinimumHeight(0);
        btn.setMinHeight(0);
        btn.setBackgroundTintList(null);
        btn.setStateListAnimator(null);
        return btn;
    }

    private void updateChannelButtons() {
        if (chanTxButtons == null) return;
        for (int i = 0; i < MAX_CHANNELS; i++) {
            int ch = i + 1;
            String label = String.format(Locale.US, "%02d", ch);

            Button txBtn = chanTxButtons[i];
            if (txBtn != null) {
                GradientDrawable txBg = new GradientDrawable();
                txBg.setCornerRadius(dpToPx(3));
                txBg.setStroke(dpToPx(2), cPri);
                if (ch == txChannel) {
                    txBg.setColor(cPri);
                    txBtn.setTextColor(0xFF000000);
                } else {
                    txBg.setColor(cBg);
                    txBtn.setTextColor(cPri);
                }
                txBtn.setText(label + "TX");
                txBtn.setBackground(txBg);
            }

            Button rxBtn = chanRxButtons[i];
            if (rxBtn != null) {
                GradientDrawable rxBg = new GradientDrawable();
                rxBg.setCornerRadius(dpToPx(3));
                rxBg.setStroke(dpToPx(2), cPri);
                if (rxChannels.contains(ch)) {
                    rxBg.setColor(cPri);
                    rxBtn.setTextColor(0xFF000000);
                } else {
                    rxBg.setColor(cBg);
                    rxBtn.setTextColor(cPri);
                }
                rxBtn.setText(label + "RX");
                rxBtn.setBackground(rxBg);
            }
        }
    }

    @SuppressWarnings("deprecation")
    private void hideSystemUI() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            getWindow().setDecorFitsSystemWindows(false);
            WindowInsetsController controller = getWindow().getInsetsController();
            if (controller != null) {
                controller.hide(WindowInsets.Type.statusBars() | WindowInsets.Type.navigationBars());
                controller.setSystemBarsBehavior(WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE);
            }
        } else {
            getWindow().getDecorView().setSystemUiVisibility(
                    View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
                    | View.SYSTEM_UI_FLAG_FULLSCREEN
                    | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                    | View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                    | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION
                    | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN);
        }
    }

    @Override
    public void onWindowFocusChanged(boolean hasFocus) {
        super.onWindowFocusChanged(hasFocus);
        if (hasFocus) hideSystemUI();
    }

    @Override
    protected void onResume() {
        super.onResume();
        hideSystemUI();
        polling = true;
        handler.post(pollRunnable);
    }

    @Override
    protected void onPause() {
        super.onPause();
        polling = false;
        handler.removeCallbacks(pollRunnable);
        resetHomeButton();
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        super.onSaveInstanceState(outState);
        outState.putInt("page", page);
        outState.putBoolean("nvg_mode", nvgMode);
    }

    @Override
    protected void onDestroy() {
        super.onDestroy();
        executor.shutdownNow();
    }

    @Override
    public boolean onKeyDown(int keyCode, KeyEvent event) {
        if (keyCode == KeyEvent.KEYCODE_VOLUME_UP) {
            adjustRadioVolume("spk", VOL_STEP);
            return true;
        }
        if (keyCode == KeyEvent.KEYCODE_VOLUME_DOWN) {
            adjustRadioVolume("spk", -VOL_STEP);
            return true;
        }
        return super.onKeyDown(keyCode, event);
    }

    private void goToPage(int p) {
        page = p;
        updateDisplay();
    }

    private void adjustRadioVolume(String type, int delta) {
        lastUserAction = System.currentTimeMillis();
        if ("mic".equals(type)) {
            micVolume = Math.max(0, Math.min(100, micVolume + delta));
        } else {
            spkVolume = Math.max(0, Math.min(100, spkVolume + delta));
        }
        updateDisplay();

        int mic = micVolume;
        int spk = spkVolume;
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String body = "{\"action\":\"volume\",\"mic_volume\":\"" + mic + "\",\"speaker_volume\":\"" + spk + "\"}";
                postJson(base + "/api/voice", body);
            } catch (Exception ignored) {}
        });
    }

    private void setTxChannel(int ch) {
        lastUserAction = System.currentTimeMillis();
        txChannel = ch;
        updateDisplay();
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                postJson(base + "/api/voice/channels", "{\"tx\":" + ch + "}");
            } catch (Exception ignored) {}
        });
    }

    private void toggleRxChannel(int ch) {
        if (ch == txChannel) return;
        lastUserAction = System.currentTimeMillis();
        if (rxChannels.contains(ch)) {
            rxChannels.remove(ch);
        } else {
            rxChannels.add(ch);
        }
        updateDisplay();

        StringBuilder rxArr = new StringBuilder("[");
        boolean first = true;
        for (int c : rxChannels) {
            if (!first) rxArr.append(",");
            rxArr.append(c);
            first = false;
        }
        rxArr.append("]");

        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                postJson(base + "/api/voice/channels", "{\"tx\":" + txChannel + ",\"rx\":" + rxArr + "}");
            } catch (Exception ignored) {}
        });
    }

    private void parseRxChannels(JSONObject chData) {
        rxChannels.clear();
        JSONArray rx = chData.optJSONArray("rx_channels");
        if (rx != null) {
            for (int i = 0; i < rx.length(); i++) {
                rxChannels.add(rx.optInt(i));
            }
        }
    }

    private void pollData() {
        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String dataJson = fetchJson(base + "/api/data");
                String localJson = fetchJson(base + "/api/local");
                String voiceJson = null;
                try { voiceJson = fetchJson(base + "/api/voice"); } catch (Exception ignored) {}
                String channelsJson = null;
                try { channelsJson = fetchJson(base + "/api/voice/channels"); } catch (Exception ignored) {}

                int unread = 0;
                boolean chatOk = false;
                JSONArray msgs = null;
                String chatHost = "";
                try {
                    String chatJson = fetchJson(base + "/api/applets/mesh-chat/proxy/unread");
                    JSONObject chat = new JSONObject(chatJson);
                    unread = chat.optInt("count", 0);
                    chatOk = true;
                } catch (Exception ignored) {}

                List<String> peerNames = new ArrayList<>();
                if (chatOk && page == 4) {
                    try {
                        String msgsJson = fetchJson(base + "/api/applets/mesh-chat/proxy/messages");
                        msgs = new JSONArray(msgsJson);
                        String healthJson = fetchJson(base + "/api/applets/mesh-chat/proxy/health");
                        JSONObject health = new JSONObject(healthJson);
                        chatHost = health.optString("hostname", "");
                    } catch (Exception ignored) {}
                    try {
                        String peersJson = fetchJson(base + "/api/applets/mesh-chat/proxy/peers");
                        JSONArray peersArr = new JSONArray(peersJson);
                        for (int i = 0; i < peersArr.length(); i++) {
                            JSONObject p = peersArr.optJSONObject(i);
                            if (p != null) peerNames.add(p.optString("hostname", ""));
                        }
                    } catch (Exception ignored) {}
                }

                JSONObject dObj = new JSONObject(dataJson);
                JSONObject lObj = new JSONObject(localJson);
                JSONObject vObj = voiceJson != null ? new JSONObject(voiceJson) : null;
                JSONObject cObj = channelsJson != null ? new JSONObject(channelsJson) : null;
                int ur = unread;
                boolean chatOkFinal = chatOk;
                JSONArray msgsFinal = msgs;
                String chatHostFinal = chatHost;
                List<String> peersFinal = peerNames;

                runOnUiThread(() -> {
                    apiData = dObj;
                    apiLocal = lObj;
                    apiVoice = vObj;
                    apiChannels = cObj;
                    chatUnread = ur;
                    chatAvailable = chatOkFinal;

                    if (msgsFinal != null) {
                        int prevCount = chatMessages != null ? chatMessages.length() : 0;
                        chatMessages = msgsFinal;
                        if (msgsFinal.length() > prevCount && page != 4) {
                            for (int i = prevCount; i < msgsFinal.length(); i++) {
                                JSONObject m = msgsFinal.optJSONObject(i);
                                if (m != null && "file".equals(m.optString("type"))) {
                                    String fname = "";
                                    JSONObject f = m.optJSONObject("file");
                                    if (f != null) fname = f.optString("name", "file");
                                    Toast.makeText(EudActivity.this,
                                            "FILE: " + fname + " from " + m.optString("from", "?"),
                                            Toast.LENGTH_SHORT).show();
                                    break;
                                }
                            }
                        }
                    }

                    if (!chatHostFinal.isEmpty()) chatHostname = chatHostFinal;
                    if (!peersFinal.isEmpty()) {
                        peersFinal.remove(chatHostname);
                        chatPeers = peersFinal;
                        if (dmTargetIdx >= chatPeers.size()) {
                            dmTargetIdx = -1;
                            if (dmTargetBtn != null) dmTargetBtn.setText("ALL");
                        }
                    }
                    connected = true;
                    wasConnected = true;
                    lastPollMs = System.currentTimeMillis();

                    if (apiLocal != null) {
                        nodeHostname = apiLocal.optString("hostname", "");
                    }
                    if (vObj != null) {
                        pttConnected = vObj.optBoolean("ptt_connected", false);
                    }

                    boolean debounced = System.currentTimeMillis() - lastUserAction < DEBOUNCE_MS;
                    if (!debounced) {
                        if (vObj != null) {
                            try { micVolume = Integer.parseInt(vObj.optString("mic_volume", "80")); } catch (NumberFormatException ignored) {}
                            try { spkVolume = Integer.parseInt(vObj.optString("speaker_volume", "80")); } catch (NumberFormatException ignored) {}
                        }
                        if (cObj != null) {
                            txChannel = cObj.optInt("tx_channel", txChannel);
                            parseRxChannels(cObj);
                        }
                    }
                    updateDisplay();
                });
            } catch (Exception e) {
                runOnUiThread(() -> {
                    connected = false;
                    updateDisplay();
                });
            }
        });
    }

    private String lastUpdateTime() {
        if (lastPollMs <= 0) return "--";
        SimpleDateFormat sdf = new SimpleDateFormat("dd/MM HH:mm:ss", Locale.US);
        return sdf.format(new Date(lastPollMs));
    }

    private void updateDisplay() {
        String titleHost = nodeHostname.isEmpty() ? "" : " " + truncate(nodeHostname, 16);
        tvTitle.setText("── " + PAGES[page] + " ──" + titleHost);
        tvConn.setText(connected ? (nvgMode ? "NVG" : "■") : "□");
        tvConn.setTextColor(connected ? cPri : 0xFF994444);

        if (offlineOverlay != null) {
            offlineOverlay.setVisibility(connected ? View.GONE : View.VISIBLE);
            offlineSubText.setVisibility(wasConnected ? View.VISIBLE : View.GONE);
        }

        StringBuilder dots = new StringBuilder();
        for (int i = 0; i < PAGES.length; i++) {
            if (i > 0) dots.append(" ");
            dots.append(i == page ? "●" : "○");
        }
        tvPages.setText(dots.toString());

        SimpleDateFormat sdf = new SimpleDateFormat("HH:mm:ss", Locale.US);
        sdf.setTimeZone(TimeZone.getDefault());
        tvTime.setText(sdf.format(new Date()));

        for (int i = 0; i < navButtons.length; i++) {
            GradientDrawable bg = new GradientDrawable();
            bg.setCornerRadius(dpToPx(4));
            if (i == page) {
                bg.setColor(cBgAct);
                bg.setStroke(dpToPx(2), cPri);
                navButtons[i].setTextColor(cPri);
            } else {
                bg.setColor(cBg);
                bg.setStroke(dpToPx(1), cDim);
                navButtons[i].setTextColor(cDim);
            }
            navButtons[i].setBackground(bg);
        }

        boolean comms = page == 2;
        boolean mesh = page == 1;
        boolean chat = page == 4;
        channelGrid.setVisibility(comms ? View.VISIBLE : View.GONE);
        if (channelScroll != null) {
            channelScroll.setVisibility(comms ? View.VISIBLE : View.GONE);
        }
        voiceRow.setVisibility(comms ? View.VISIBLE : View.GONE);
        chatInputRow.setVisibility(chat && chatAvailable ? View.VISIBLE : View.GONE);
        chatActionRow.setVisibility(chat && chatAvailable ? View.VISIBLE : View.GONE);
        templateRow.setVisibility(chat && chatAvailable ? View.VISIBLE : View.GONE);
        boolean stat = page == 3;
        if (topoContainer != null) {
            topoContainer.setVisibility((mesh || stat) ? View.VISIBLE : View.GONE);
            if (topoView != null) topoView.setVisibility(mesh ? View.VISIBLE : View.GONE);
            if (bftView != null) bftView.setVisibility(stat ? View.VISIBLE : View.GONE);
        }
        if (sidePanel != null) {
            sidePanel.setVisibility((comms || mesh || stat || (chat && chatAvailable)) ? View.VISIBLE : View.GONE);
        }
        int eudCount = 0;
        if (apiLocal != null) {
            JSONArray euds = apiLocal.optJSONArray("euds");
            if (euds != null) eudCount = euds.length();
        }
        if (mesh && topoView != null && apiData != null) {
            topoView.setData(
                    apiData.optJSONArray("nodes"),
                    apiData.optJSONArray("edges"),
                    apiData.optLong("timestamp", 0),
                    eudCount);
        }
        if (stat && bftView != null && apiData != null) {
            bftView.setData(apiData.optJSONArray("nodes"), apiData.optLong("timestamp", 0));
        }

        pttBtn.setVisibility(comms && pttConnected ? View.VISIBLE : View.GONE);
        alertBtn.setVisibility(stat ? View.VISIBLE : View.GONE);

        if (comms) updateChannelButtons();

        if (!connected && apiData == null) {
            tvDisplay.setText(
                    " CONNECTING...\n\n" +
                    " NODE: " + truncate(nodeUrl, 22) + "\n\n" +
                    " Waiting for mesh data\n");
            return;
        }

        if (chatUnread > 0 && page != 4) {
            navButtons[4].setText("CHAT(" + chatUnread + ")");
        } else {
            navButtons[4].setText("CHAT");
        }

        switch (page) {
            case 0: tvDisplay.setText(formatRadio()); break;
            case 1: tvDisplay.setText(formatMesh()); break;
            case 2: tvDisplay.setText(formatComms()); break;
            case 3: tvDisplay.setText(formatStatus()); break;
            case 4: tvDisplay.setText(formatChat()); break;
        }
    }

    private String formatRadio() {
        StringBuilder sb = new StringBuilder();
        String ssid = optLocalStr("mesh_ssid", "--");
        String freq = "--", bw = "--", tx = "--", ch = "--", state = "--", ifname = "wlan2";
        String signal = "--";
        if (apiLocal != null) {
            JSONArray ifaces = apiLocal.optJSONArray("interfaces");
            if (ifaces != null) {
                for (int i = 0; i < ifaces.length(); i++) {
                    JSONObject ifc = ifaces.optJSONObject(i);
                    if (ifc != null && "mesh".equals(ifc.optString("role"))) {
                        freq = ifc.optString("freq_mhz", "--");
                        bw = ifc.optString("halow_bw", "--");
                        tx = ifc.optString("txpower_dbm", "--");
                        ch = ifc.optString("channel", "--");
                        state = ifc.optString("state", "--").toUpperCase();
                        ifname = ifc.optString("name", "wlan2");
                        signal = ifc.optString("signal_dbm", "--");
                        break;
                    }
                }
            }
        }
        sb.append(" ┌─ RF LINK ───────────┐\n");
        sb.append(cardRow("SSID", ssid));
        sb.append(cardRow("CHAN", ch + " / " + (freq.equals("--") ? "--" : freq + " MHz")));
        sb.append(cardRow("BW", bw));
        sb.append(cardRow("TX PWR", tx.equals("--") ? "--" : tx + " dBm"));
        sb.append(cardRow("SIGNAL", signal.equals("--") ? "--" : signal + " dBm"));
        sb.append(cardRow("STATE", ifname + " " + state));
        sb.append(" └──────────────────────┘\n");
        sb.append(" ┌─ SYSTEM ────────────┐\n");
        sb.append(cardRow("MODE", nvgMode ? "NVG RED" : "STD GRN"));
        sb.append(cardRow("UPD", lastUpdateTime()));
        sb.append(" └──────────────────────┘\n");
        return sb.toString();
    }

    private String cardRow(String label, String value) {
        return String.format(Locale.US, " │ %-5s %s\n", label, value);
    }

    private String formatMesh() {
        StringBuilder sb = new StringBuilder();
        int total = 0, online = 0, maxHops = 0;
        String selfIp = "--", gw = "--", bestTp = "--", worstTp = "--";
        if (apiLocal != null) selfIp = apiLocal.optString("ip", "--");
        if (apiData != null) {
            JSONArray nodes = apiData.optJSONArray("nodes");
            if (nodes != null) {
                total = nodes.length();
                long now = apiData.optLong("timestamp", System.currentTimeMillis() / 1000);
                for (int i = 0; i < nodes.length(); i++) {
                    JSONObject n = nodes.optJSONObject(i);
                    if (n == null) continue;
                    if (n.optBoolean("is_me", false)) { online++; continue; }
                    String ls = n.optString("last_seen", "");
                    if (!ls.isEmpty()) {
                        try { if (now - Long.parseLong(ls) <= 300) online++; } catch (NumberFormatException ignored) {}
                    }
                    if (!n.isNull("hop_count")) {
                        int hops = n.optInt("hop_count", 0);
                        if (hops > maxHops) maxHops = hops;
                    }
                }
                for (int i = 0; i < nodes.length(); i++) {
                    JSONObject n = nodes.optJSONObject(i);
                    if (n != null && n.optBoolean("is_gateway", false)) {
                        gw = n.optString("ip", n.optString("hostname", "--"));
                        break;
                    }
                }
            }
            JSONArray edges = apiData.optJSONArray("edges");
            double maxTp = 0, minTp = Double.MAX_VALUE;
            if (edges != null) {
                for (int i = 0; i < edges.length(); i++) {
                    JSONObject e = edges.optJSONObject(i);
                    if (e != null && !e.isNull("throughput")) {
                        double tp = e.optDouble("throughput", 0);
                        if (tp <= 0) continue;
                        if (tp > maxTp) maxTp = tp;
                        if (tp < minTp) minTp = tp;
                    }
                }
            }
            if (maxTp > 0) bestTp = String.format(Locale.US, "%.1f Mbit/s", maxTp);
            if (minTp < Double.MAX_VALUE) worstTp = String.format(Locale.US, "%.1f Mbit/s", minTp);
            String selGw = apiData.optString("selected_gw", "");
            if (!selGw.isEmpty()) gw = selGw;
        }
        sb.append(" ┌─ TOPOLOGY ──────────┐\n");
        sb.append(cardRow("NODES", online + "/" + total + " ONLINE"));
        sb.append(cardRow("SELF", selfIp));
        sb.append(cardRow("GW", gw));
        sb.append(cardRow("GWs", apiData != null ? "" + apiData.optInt("gateway_count", 0) : "--"));
        sb.append(cardRow("HOP", maxHops > 0 ? "" + maxHops : "--"));
        sb.append(" └──────────────────────┘\n");
        sb.append(" ┌─ PERFORMANCE ───────┐\n");
        sb.append(cardRow("BEST", bestTp));
        sb.append(cardRow("WORST", worstTp));
        sb.append(cardRow("UPD", lastUpdateTime()));
        sb.append(" └──────────────────────┘\n");
        return sb.toString();
    }

    private String formatComms() {
        StringBuilder sb = new StringBuilder();
        String voiceState = "--", pttState = "--";
        if (apiVoice != null) {
            voiceState = apiVoice.optBoolean("active", false) ? "ACTIVE" : "STOPPED";
            pttState = apiVoice.optBoolean("ptt_active", false) ? "TX ■" : "RX";
        }
        sb.append(row("VOICE", voiceState + "  " + pttState));
        sb.append(row("TX CH", String.valueOf(txChannel)));

        StringBuilder rxStr = new StringBuilder();
        for (int c : rxChannels) {
            if (rxStr.length() > 0) rxStr.append(",");
            rxStr.append(c);
        }
        sb.append(row("RX CH", rxStr.length() > 0 ? rxStr.toString() : "--"));
        sb.append(row("MIC", micVolume + "%"));
        sb.append(row("VOL", spkVolume + "%"));

        if (apiChannels != null) {
            JSONArray chArr = apiChannels.optJSONArray("channels");
            if (chArr != null) {
                StringBuilder active = new StringBuilder();
                for (int i = 0; i < chArr.length(); i++) {
                    JSONObject ch = chArr.optJSONObject(i);
                    if (ch != null && ch.optBoolean("active", false)) {
                        if (active.length() > 0) active.append(",");
                        active.append(ch.optInt("channel"));
                    }
                }
                sb.append(row("ACTV", active.length() > 0 ? active.toString() : "NONE"));
            }

            int lastTx = apiChannels.optInt("last_tx_channel", 0);
            long lastTxAt = apiChannels.optLong("last_tx_at", 0);
            int lastRx = apiChannels.optInt("last_rx_channel", 0);
            long lastRxAt = apiChannels.optLong("last_rx_at", 0);
            long now = System.currentTimeMillis();
            sb.append(row("LSTTX", lastTx > 0 ? "CH" + lastTx + " " + agoStr(now, lastTxAt) : "--"));
            sb.append(row("LSTRX", lastRx > 0 ? "CH" + lastRx + " " + agoStr(now, lastRxAt) : "--"));
        }

        sb.append(row("PTT", pttConnected ? "CONNECTED" : "---"));
        sb.append(row("CHAT", chatUnread > 0 ? chatUnread + " UNREAD" : "0 UNREAD"));
        return sb.toString();
    }

    private String agoStr(long nowMs, long atMs) {
        if (atMs <= 0) return "";
        long sec = (nowMs - atMs) / 1000;
        if (sec < 5) return "NOW";
        if (sec < 60) return sec + "s ago";
        if (sec < 3600) return (sec / 60) + "m ago";
        return (sec / 3600) + "h ago";
    }

    private String formatStatus() {
        StringBuilder sb = new StringBuilder();
        String uptime = "--", hostname = "--";
        boolean usbTether = false, isGw = false;
        String eudMode = "--", gps = "NO FIX";
        if (apiLocal != null) {
            uptime = apiLocal.optString("uptime", "--");
            hostname = apiLocal.optString("hostname", "--");
            eudMode = apiLocal.optString("eud_mode", "--").toUpperCase();
            JSONObject net = apiLocal.optJSONObject("network");
            if (net != null) { usbTether = net.optBoolean("usb_tether", false); isGw = net.optBoolean("gateway", false); }
            JSONObject gpsObj = apiLocal.optJSONObject("gps");
            if (gpsObj != null && gpsObj.optBoolean("connected", false)) {
                String lat = gpsObj.optString("lat", ""), lon = gpsObj.optString("lon", "");
                gps = (!lat.isEmpty() && !lon.isEmpty()) ? lat + "," + lon : "CONNECTED";
            }
        }
        sb.append(" ┌─ NODE ──────────────┐\n");
        sb.append(cardRow("HOST", truncate(hostname, 18)));
        sb.append(cardRow("UP", uptime));
        sb.append(cardRow("EUD", eudMode));
        sb.append(cardRow("GW", isGw ? "YES" : "NO"));
        if (usbTether) sb.append(cardRow("USB", "TETHERED"));
        sb.append(" └──────────────────────┘\n");
        sb.append(" ┌─ POSITION ──────────┐\n");
        sb.append(cardRow("GPS", gps));
        sb.append(cardRow("UPD", lastUpdateTime()));
        sb.append(" └──────────────────────┘\n");
        return sb.toString();
    }

    private String formatChat() {
        if (!chatAvailable) {
            return " MESH CHAT\n\n" +
                   " Applet not installed.\n" +
                   " Install mesh-chat from\n" +
                   " the Applets tab.\n";
        }
        if (chatMessages == null || chatMessages.length() == 0) {
            return " MESH CHAT\n\n" +
                   " No messages.\n";
        }
        StringBuilder sb = new StringBuilder();
        int start = Math.max(0, chatMessages.length() - 12);
        for (int i = start; i < chatMessages.length(); i++) {
            JSONObject msg = chatMessages.optJSONObject(i);
            if (msg == null) continue;
            String type = msg.optString("type", "text");
            if (!"text".equals(type) && !"file".equals(type)) continue;

            String from = msg.optString("from", "?");
            boolean isMe = from.equals(chatHostname);
            long ts = msg.optLong("ts", 0);
            String time = "";
            if (ts > 0) {
                SimpleDateFormat tf = new SimpleDateFormat("HH:mm", Locale.US);
                time = tf.format(new Date(ts * 1000));
            }

            String body;
            if ("file".equals(type)) {
                JSONObject file = msg.optJSONObject("file");
                body = "[FILE] " + (file != null ? file.optString("name", "?") : "?");
            } else {
                body = msg.optString("body", "");
            }

            String prefix = isMe ? " > " : " " + truncate(from, 8) + ": ";
            String line = time + prefix + body;
            if (line.length() > 38) line = line.substring(0, 38);
            sb.append(line).append("\n");
        }
        return sb.toString();
    }

    private void sendChatMessage() {
        String text = chatInput.getText().toString().trim();
        if (text.isEmpty()) return;
        chatInput.setText("");
        String target = getDmTarget();

        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String escaped = text.replace("\\", "\\\\").replace("\"", "\\\"");
                String body;
                if (target != null) {
                    String tEsc = target.replace("\\", "\\\\").replace("\"", "\\\"");
                    body = "{\"body\":\"" + escaped + "\",\"to\":[\"" + tEsc + "\"]}";
                } else {
                    body = "{\"body\":\"" + escaped + "\"}";
                }
                postJson(base + "/api/applets/mesh-chat/proxy/send", body);
            } catch (Exception ignored) {}
        });
    }

    private String row(String label, String value) {
        return String.format(Locale.US, " %-6s %s\n", label, value);
    }

    private String optLocalStr(String key, String def) {
        if (apiLocal != null) { String v = apiLocal.optString(key, ""); if (!v.isEmpty()) return v; }
        return def;
    }

    private String truncate(String s, int max) {
        if (s == null) return "--";
        return s.length() > max ? s.substring(0, max) : s;
    }

    private int dpToPx(int dp) {
        return (int) (dp * getResources().getDisplayMetrics().density + 0.5f);
    }

    private String fetchJson(String urlStr) throws Exception {
        HttpURLConnection conn = openConnection(urlStr);
        conn.setConnectTimeout(5000);
        conn.setReadTimeout(5000);
        BufferedReader reader = new BufferedReader(new InputStreamReader(conn.getInputStream()));
        StringBuilder sb = new StringBuilder();
        String line;
        while ((line = reader.readLine()) != null) sb.append(line);
        reader.close();
        return sb.toString();
    }

    private void postJson(String urlStr, String body) throws Exception {
        HttpURLConnection conn = openConnection(urlStr);
        conn.setRequestMethod("POST");
        conn.setRequestProperty("Content-Type", "application/json");
        conn.setDoOutput(true);
        conn.setConnectTimeout(5000);
        conn.setReadTimeout(5000);
        OutputStream os = conn.getOutputStream();
        os.write(body.getBytes(StandardCharsets.UTF_8));
        os.close();
        conn.getResponseCode();
        conn.disconnect();
    }

    private HttpURLConnection openConnection(String urlStr) throws Exception {
        TrustManager[] trustAll = new TrustManager[]{new X509TrustManager() {
            public void checkClientTrusted(java.security.cert.X509Certificate[] c, String a) {}
            public void checkServerTrusted(java.security.cert.X509Certificate[] c, String a) {}
            public java.security.cert.X509Certificate[] getAcceptedIssuers() {
                return new java.security.cert.X509Certificate[0];
            }
        }};
        SSLContext sc = SSLContext.getInstance("TLS");
        sc.init(null, trustAll, new java.security.SecureRandom());
        URL url = new URL(urlStr);
        if ("https".equals(url.getProtocol())) {
            HttpsURLConnection https = (HttpsURLConnection) url.openConnection();
            https.setSSLSocketFactory(sc.getSocketFactory());
            https.setHostnameVerifier((h, s) -> true);
            return https;
        }
        return (HttpURLConnection) url.openConnection();
    }
}
