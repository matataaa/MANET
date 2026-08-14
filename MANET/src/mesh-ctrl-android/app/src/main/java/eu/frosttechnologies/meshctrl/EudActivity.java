package eu.frosttechnologies.meshctrl;

import android.annotation.SuppressLint;
import android.content.Intent;
import android.graphics.Typeface;
import android.graphics.drawable.ClipDrawable;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.LayerDrawable;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
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
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import androidx.appcompat.app.ActionBar;
import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.HashSet;
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
    private static final long HOLD_MS = 7000;
    private static final long DEBOUNCE_MS = 4000;
    private static final String[] PAGES = {"RADIO", "MESH", "COMMS", "STATUS", "CHAT"};

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

    private int txChannel = 1;
    private Set<Integer> rxChannels = new HashSet<>();
    private int micVolume = 80;
    private int spkVolume = 80;

    private long lastUserAction = 0;

    private TextView tvTitle, tvConn, tvDisplay, tvPages, tvTime;
    private LinearLayout voiceRow, channelGrid, chatInputRow;
    private LinearLayout sidePanel;
    private EditText chatInput;
    private Button chatSendBtn;
    private Button[] navButtons;
    private Button[] chanTxButtons;
    private Button[] chanRxButtons;
    private Button homeBtn;

    // HOME hold state
    private long homeDownTime = 0;
    private ClipDrawable homeClip;
    private GradientDrawable homeBaseBg;

    private final Handler handler = new Handler(Looper.getMainLooper());
    private final ExecutorService executor = Executors.newSingleThreadExecutor();

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
            int level = (int) (elapsed * 10000 / HOLD_MS);
            homeClip.setLevel(level);
            handler.postDelayed(this, 50);
        }
    };

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        setContentView(R.layout.activity_eud);

        hideSystemUI();

        ActionBar ab = getSupportActionBar();
        if (ab != null) ab.hide();

        nodeUrl = getSharedPreferences("mesh_ctrl", MODE_PRIVATE)
                .getString("node_url", "https://radio.mesh");

        tvTitle = findViewById(R.id.eud_title);
        tvConn = findViewById(R.id.eud_conn);
        tvDisplay = findViewById(R.id.eud_display);
        tvPages = findViewById(R.id.eud_pages);
        tvTime = findViewById(R.id.eud_time);
        voiceRow = findViewById(R.id.eud_voice_row);
        channelGrid = findViewById(R.id.eud_channel_grid);
        chatInputRow = findViewById(R.id.eud_chat_input_row);
        sidePanel = findViewById(R.id.eud_side_panel);
        chatInput = findViewById(R.id.eud_chat_input);
        chatSendBtn = findViewById(R.id.eud_chat_send);
        chatSendBtn.setBackgroundTintList(null);
        chatSendBtn.setStateListAnimator(null);
        chatSendBtn.setOnClickListener(v -> sendChatMessage());
        chatInput.setOnEditorActionListener((v, actionId, event) -> {
            if (actionId == EditorInfo.IME_ACTION_SEND) { sendChatMessage(); return true; }
            return false;
        });

        // Voice control buttons — radio hardware volume via API
        int[] voiceBtnIds = {R.id.eud_btn_mic_up, R.id.eud_btn_mic_down, R.id.eud_btn_vol_up, R.id.eud_btn_vol_down};
        for (int id : voiceBtnIds) {
            Button b = findViewById(id);
            b.setBackgroundTintList(null);
            b.setStateListAnimator(null);
        }
        findViewById(R.id.eud_btn_mic_up).setOnClickListener(v -> adjustRadioVolume("mic", VOL_STEP));
        findViewById(R.id.eud_btn_mic_down).setOnClickListener(v -> adjustRadioVolume("mic", -VOL_STEP));
        findViewById(R.id.eud_btn_vol_up).setOnClickListener(v -> adjustRadioVolume("spk", VOL_STEP));
        findViewById(R.id.eud_btn_vol_down).setOnClickListener(v -> adjustRadioVolume("spk", -VOL_STEP));

        // Page nav buttons
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

        // HOME button — tap = Android home, hold 7s = management
        homeBtn = findViewById(R.id.eud_nav_exit);
        homeBtn.setBackgroundTintList(null);
        homeBtn.setStateListAnimator(null);
        setupHomeButton();

        buildChannelGrid();
        updateDisplay();
    }

    private void setupHomeButton() {
        homeBaseBg = new GradientDrawable();
        homeBaseBg.setCornerRadius(dpToPx(4));
        homeBaseBg.setColor(0xFF0a1a0a);
        homeBaseBg.setStroke(dpToPx(1), 0xFF1a5a1a);

        GradientDrawable fillShape = new GradientDrawable();
        fillShape.setCornerRadius(dpToPx(4));
        fillShape.setColor(0xFF33FF33);
        homeClip = new ClipDrawable(fillShape, Gravity.LEFT, ClipDrawable.HORIZONTAL);
        homeClip.setLevel(0);

        LayerDrawable layers = new LayerDrawable(new android.graphics.drawable.Drawable[]{homeBaseBg, homeClip});
        homeBtn.setBackground(layers);

        homeBtn.setOnTouchListener((v, event) -> {
            switch (event.getAction()) {
                case MotionEvent.ACTION_DOWN:
                    homeDownTime = System.currentTimeMillis();
                    homeBtn.setText("  MGMT");
                    homeBtn.setGravity(Gravity.START | Gravity.CENTER_VERTICAL);
                    homeBtn.setTextColor(0xFF33FF33);
                    handler.post(homeTickRunnable);
                    return true;
                case MotionEvent.ACTION_UP:
                case MotionEvent.ACTION_CANCEL:
                    long held = System.currentTimeMillis() - homeDownTime;
                    resetHomeButton();
                    if (held < 500) {
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
        homeClip.setLevel(0);
        homeBtn.setText("HOME");
        homeBtn.setGravity(Gravity.CENTER);
        homeBtn.setTextColor(0xFF33FF33);
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
        btn.setTextColor(0xFF33FF33);
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
                txBg.setStroke(dpToPx(2), 0xFF33FF33);
                if (ch == txChannel) {
                    txBg.setColor(0xFF33FF33);
                    txBtn.setTextColor(0xFF000000);
                } else {
                    txBg.setColor(0xFF0a1a0a);
                    txBtn.setTextColor(0xFF33FF33);
                }
                txBtn.setText(label + "TX");
                txBtn.setBackground(txBg);
            }

            Button rxBtn = chanRxButtons[i];
            if (rxBtn != null) {
                GradientDrawable rxBg = new GradientDrawable();
                rxBg.setCornerRadius(dpToPx(3));
                rxBg.setStroke(dpToPx(2), 0xFF33FF33);
                if (rxChannels.contains(ch)) {
                    rxBg.setColor(0xFF33FF33);
                    rxBtn.setTextColor(0xFF000000);
                } else {
                    rxBg.setColor(0xFF0a1a0a);
                    rxBtn.setTextColor(0xFF33FF33);
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

                if (chatOk && page == 4) {
                    try {
                        String msgsJson = fetchJson(base + "/api/applets/mesh-chat/proxy/messages");
                        msgs = new JSONArray(msgsJson);
                        String healthJson = fetchJson(base + "/api/applets/mesh-chat/proxy/health");
                        JSONObject health = new JSONObject(healthJson);
                        chatHost = health.optString("hostname", "");
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

                runOnUiThread(() -> {
                    apiData = dObj;
                    apiLocal = lObj;
                    apiVoice = vObj;
                    apiChannels = cObj;
                    chatUnread = ur;
                    chatAvailable = chatOkFinal;
                    if (msgsFinal != null) chatMessages = msgsFinal;
                    if (!chatHostFinal.isEmpty()) chatHostname = chatHostFinal;
                    connected = true;

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

    private void updateDisplay() {
        tvTitle.setText("── " + PAGES[page] + " ──");
        tvConn.setText(connected ? "■" : "□");
        tvConn.setTextColor(connected ? 0xFF33FF33 : 0xFF994444);

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
                bg.setColor(0xFF0a2a0a);
                bg.setStroke(dpToPx(2), 0xFF33FF33);
                navButtons[i].setTextColor(0xFF33FF33);
            } else {
                bg.setColor(0xFF0a1a0a);
                bg.setStroke(dpToPx(1), 0xFF1a5a1a);
                navButtons[i].setTextColor(0xFF1a5a1a);
            }
            navButtons[i].setBackground(bg);
        }

        boolean comms = page == 2;
        boolean chat = page == 4;
        channelGrid.setVisibility(comms ? View.VISIBLE : View.GONE);
        voiceRow.setVisibility(comms ? View.VISIBLE : View.GONE);
        chatInputRow.setVisibility(chat && chatAvailable ? View.VISIBLE : View.GONE);
        if (sidePanel != null) {
            sidePanel.setVisibility(comms ? View.VISIBLE : View.GONE);
        }

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
                        break;
                    }
                }
            }
        }
        sb.append(row("SSID", ssid));
        sb.append(row("CHAN", ch));
        sb.append(row("FREQ", freq.equals("--") ? "--" : freq + " MHz"));
        sb.append(row("BW", bw));
        sb.append(row("TX", tx.equals("--") ? "--" : tx + " dBm"));
        sb.append(row("LINK", ifname + " " + state));
        return sb.toString();
    }

    private String formatMesh() {
        StringBuilder sb = new StringBuilder();
        int total = 0, online = 0;
        String selfIp = "--", gw = "--", bestTp = "--";
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
            double maxTp = 0;
            if (edges != null) {
                for (int i = 0; i < edges.length(); i++) {
                    JSONObject e = edges.optJSONObject(i);
                    if (e != null && !e.isNull("throughput")) {
                        double tp = e.optDouble("throughput", 0);
                        if (tp > maxTp) maxTp = tp;
                    }
                }
            }
            if (maxTp > 0) bestTp = String.format(Locale.US, "%.1f Mbit/s", maxTp);
            String selGw = apiData.optString("selected_gw", "");
            if (!selGw.isEmpty()) gw = selGw;
        }
        sb.append(row("NODES", online + "/" + total + " ONLINE"));
        sb.append(row("SELF", selfIp));
        sb.append(row("GW", gw));
        sb.append(row("BEST", bestTp));
        sb.append(row("GWs", apiData != null ? "" + apiData.optInt("gateway_count", 0) : "--"));
        sb.append(row("PROTO", "BATMAN_V"));
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
        }

        sb.append(row("CHAT", chatUnread > 0 ? chatUnread + " UNREAD" : "0 UNREAD"));
        return sb.toString();
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
        sb.append(row("UP", uptime));
        sb.append(row("HOST", truncate(hostname, 20)));
        sb.append(row("EUD", eudMode));
        sb.append(row("GW", isGw ? "YES" : "NO"));
        sb.append(row("USB", usbTether ? "TETHERED ▲" : "---"));
        sb.append(row("GPS", gps));
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

        executor.execute(() -> {
            try {
                String base = nodeUrl.replaceAll("/$", "");
                String escaped = text.replace("\\", "\\\\").replace("\"", "\\\"");
                postJson(base + "/api/applets/mesh-chat/proxy/send",
                        "{\"body\":\"" + escaped + "\"}");
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
