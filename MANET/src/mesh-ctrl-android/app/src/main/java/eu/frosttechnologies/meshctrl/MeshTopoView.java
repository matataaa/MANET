package eu.frosttechnologies.meshctrl;

import android.annotation.SuppressLint;
import android.content.Context;
import android.graphics.Canvas;
import android.graphics.Paint;
import android.graphics.Typeface;
import android.view.MotionEvent;
import android.view.View;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

@SuppressLint("ClickableViewAccessibility")
public class MeshTopoView extends View {

    public interface OnNodeTapListener {
        void onNodeTap(String hostname, String ip, boolean isMe);
    }

    private final Paint dotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint selfDotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint gwDotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint offDotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint offGlowPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint offTextPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint linePaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint textPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint dimTextPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint glowPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint linkTextPaint = new Paint(Paint.ANTI_ALIAS_FLAG);

    private boolean nvgMode = false;
    private JSONArray nodes;
    private JSONArray edges;
    private long timestamp;
    private int eudCount;
    private OnNodeTapListener nodeTapListener;
    private float[] lastPx, lastPy;
    private int lastNodeCount;

    public MeshTopoView(Context context) {
        super(context);
        applyColorScheme();
        setLayerType(LAYER_TYPE_SOFTWARE, null);
        setOnTouchListener((v, event) -> {
            if (event.getAction() == MotionEvent.ACTION_DOWN) {
                handleTap(event.getX(), event.getY());
            }
            return true;
        });
    }

    public void setOnNodeTapListener(OnNodeTapListener listener) {
        this.nodeTapListener = listener;
    }

    private void handleTap(float tx, float ty) {
        if (nodeTapListener == null || nodes == null || lastPx == null) return;
        float hitR = 60f;
        for (int i = 0; i < lastNodeCount; i++) {
            float dx = tx - lastPx[i];
            float dy = ty - lastPy[i];
            if (dx * dx + dy * dy < hitR * hitR) {
                JSONObject nd = nodes.optJSONObject(i);
                if (nd != null) {
                    nodeTapListener.onNodeTap(
                            nd.optString("hostname", "?"),
                            nd.optString("ip", ""),
                            nd.optBoolean("is_me", false));
                }
                return;
            }
        }
    }

    public void setNvgMode(boolean nvg) {
        this.nvgMode = nvg;
        applyColorScheme();
        invalidate();
    }

    private void applyColorScheme() {
        int pri = nvgMode ? 0xFFCC3333 : 0xFF33FF33;
        int dim = nvgMode ? 0xFF5a1a1a : 0xFF1a5a1a;
        int priMid = nvgMode ? 0xFFAA3333 : 0xFF33AA33;
        int priBright = nvgMode ? 0xFFFF4444 : 0xFF66FF66;

        dotPaint.setColor(pri);
        dotPaint.setStyle(Paint.Style.FILL);

        selfDotPaint.setColor(pri);
        selfDotPaint.setStyle(Paint.Style.FILL);
        selfDotPaint.setShadowLayer(12, 0, 0, pri);

        gwDotPaint.setColor(nvgMode ? 0xFFFF9933 : 0xFF33CCFF);
        gwDotPaint.setStyle(Paint.Style.FILL);
        gwDotPaint.setShadowLayer(10, 0, 0, nvgMode ? 0xFFFF9933 : 0xFF33CCFF);

        offDotPaint.setColor(0xFFCC3333);
        offDotPaint.setStyle(Paint.Style.STROKE);
        offDotPaint.setStrokeWidth(2f);

        offGlowPaint.setColor(0x30CC3333);
        offGlowPaint.setStyle(Paint.Style.FILL);

        offTextPaint.setColor(0xFF993333);
        offTextPaint.setTextSize(24f);
        offTextPaint.setTypeface(Typeface.MONOSPACE);

        linePaint.setColor(dim);
        linePaint.setStyle(Paint.Style.STROKE);
        linePaint.setStrokeWidth(2f);

        textPaint.setColor(pri);
        textPaint.setTextSize(28f);
        textPaint.setTypeface(Typeface.create(Typeface.MONOSPACE, Typeface.BOLD));
        textPaint.setFakeBoldText(true);
        textPaint.setShadowLayer(4, 0, 0, pri);

        dimTextPaint.setColor(priMid);
        dimTextPaint.setTextSize(24f);
        dimTextPaint.setTypeface(Typeface.MONOSPACE);

        glowPaint.setColor((0x2A << 24) | (pri & 0x00FFFFFF));
        glowPaint.setStyle(Paint.Style.FILL);

        linkTextPaint.setColor(priBright);
        linkTextPaint.setTextSize(22f);
        linkTextPaint.setTypeface(Typeface.MONOSPACE);
        linkTextPaint.setTextAlign(Paint.Align.CENTER);
        linkTextPaint.setShadowLayer(3, 0, 0, pri);
    }

    public void setData(JSONArray nodes, JSONArray edges, long timestamp, int eudCount) {
        this.nodes = nodes;
        this.edges = edges;
        this.timestamp = timestamp;
        this.eudCount = eudCount;
        invalidate();
    }

    @Override
    protected void onDraw(Canvas canvas) {
        super.onDraw(canvas);
        canvas.drawColor(nvgMode ? 0xFF0f0a0a : 0xFF0a0f0a);

        if (nodes == null || nodes.length() == 0) {
            canvas.drawText("NO NODES", getWidth() / 2f - 40, getHeight() / 2f, dimTextPaint);
            return;
        }

        int w = getWidth();
        int h = getHeight();
        int pad = 40;
        int count = nodes.length();

        List<String> ids = new ArrayList<>();
        List<String> labels = new ArrayList<>();
        List<Boolean> online = new ArrayList<>();
        List<Boolean> isMe = new ArrayList<>();
        List<Boolean> isGw = new ArrayList<>();
        Map<String, Integer> idIndex = new HashMap<>();

        for (int i = 0; i < count; i++) {
            JSONObject nd = nodes.optJSONObject(i);
            if (nd == null) continue;
            String id = nd.optString("id", nd.optString("mac", ""));
            String hostname = nd.optString("hostname", "?");
            String label = hostname.length() > 10 ? hostname.substring(0, 10) : hostname;
            boolean me = nd.optBoolean("is_me", false);
            boolean gw = nd.optBoolean("is_gateway", false);

            boolean on = me;
            if (!me) {
                String ls = nd.optString("last_seen", "");
                if (!ls.isEmpty() && timestamp > 0) {
                    try {
                        on = (timestamp - Long.parseLong(ls)) <= 300;
                    } catch (NumberFormatException ignored) {}
                }
            }

            idIndex.put(id, ids.size());
            String mac = nd.optString("mac", "");
            if (!mac.isEmpty()) idIndex.put(mac, ids.size());
            ids.add(id);
            labels.add(gw ? label + " (GW)" : label);
            online.add(on);
            isMe.add(me);
            isGw.add(gw);
        }

        int n = ids.size();
        if (n == 0) return;

        float cx = w / 2f;
        float cy = h / 2f;
        float radius = Math.min(w - pad * 2, h - pad * 2) / 2f - 20;

        float[] px = new float[n];
        float[] py = new float[n];

        if (n == 1) {
            px[0] = cx;
            py[0] = cy;
        } else {
            for (int i = 0; i < n; i++) {
                double angle = 2 * Math.PI * i / n - Math.PI / 2;
                px[i] = cx + (float) (radius * Math.cos(angle));
                py[i] = cy + (float) (radius * Math.sin(angle));
            }
        }

        lastPx = px;
        lastPy = py;
        lastNodeCount = n;

        if (edges != null) {
            for (int i = 0; i < edges.length(); i++) {
                JSONObject e = edges.optJSONObject(i);
                if (e == null) continue;
                String src = e.optString("source", e.optString("from", ""));
                String tgt = e.optString("target", e.optString("to", ""));
                Integer fi = idIndex.get(src);
                Integer ti = idIndex.get(tgt);
                if (fi == null || ti == null) continue;

                double tp = e.optDouble("throughput", 0);
                int pri = nvgMode ? 0xCC3333 : 0x33FF33;
                int dim = nvgMode ? 0x5a1a1a : 0x1a5a1a;
                if (tp > 0) {
                    int alpha = Math.min(255, (int) (tp * 25) + 80);
                    linePaint.setColor((alpha << 24) | pri);
                    linePaint.setStrokeWidth(Math.min(6f, (float) tp / 2f + 2f));
                } else {
                    linePaint.setColor(0x80000000 | dim);
                    linePaint.setStrokeWidth(2f);
                }
                canvas.drawLine(px[fi], py[fi], px[ti], py[ti], linePaint);

                if (tp > 0) {
                    float mx = (px[fi] + px[ti]) / 2f;
                    float my = (py[fi] + py[ti]) / 2f;
                    String tpStr = String.format(Locale.US, "%.1f Mb", tp);
                    canvas.drawText(tpStr, mx, my - 6, linkTextPaint);
                }
            }
        }

        float dotR = Math.max(6, Math.min(14, 120f / n));
        for (int i = 0; i < n; i++) {
            if (online.get(i)) {
                canvas.drawCircle(px[i], py[i], dotR + 6, glowPaint);
                if (isMe.get(i)) {
                    canvas.drawCircle(px[i], py[i], dotR + 2, selfDotPaint);
                } else if (isGw.get(i)) {
                    canvas.drawCircle(px[i], py[i], dotR + 1, gwDotPaint);
                } else {
                    canvas.drawCircle(px[i], py[i], dotR, dotPaint);
                }
            } else {
                canvas.drawCircle(px[i], py[i], dotR + 4, offGlowPaint);
                canvas.drawCircle(px[i], py[i], dotR, offDotPaint);
            }

            float tx = px[i] + dotR + 4;
            float ty = py[i] + 5;
            Paint lp;
            if (online.get(i)) {
                lp = textPaint;
            } else {
                lp = offTextPaint;
            }
            if (px[i] > cx) {
                tx = px[i] - lp.measureText(labels.get(i)) - dotR - 4;
            }
            canvas.drawText(labels.get(i), tx, ty, lp);
        }

        int onCount = (int) online.stream().filter(b -> b).count();
        String countStr = String.format(Locale.US, "%d/%d NODES", onCount, n);
        if (eudCount > 0) countStr += String.format(Locale.US, "  %d EUD", eudCount);
        canvas.drawText(countStr, 8, h - 8, dimTextPaint);
    }
}
