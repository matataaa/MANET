package eu.frosttechnologies.meshctrl;

import android.content.Context;
import android.graphics.Canvas;
import android.graphics.Paint;
import android.graphics.Typeface;
import android.view.View;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

public class MeshTopoView extends View {

    private final Paint dotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint selfDotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint gwDotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint linePaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint textPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint dimTextPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint glowPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint linkTextPaint = new Paint(Paint.ANTI_ALIAS_FLAG);

    private JSONArray nodes;
    private JSONArray edges;
    private long timestamp;
    private int eudCount;

    public MeshTopoView(Context context) {
        super(context);
        dotPaint.setColor(0xFF33FF33);
        dotPaint.setStyle(Paint.Style.FILL);

        selfDotPaint.setColor(0xFF33FF33);
        selfDotPaint.setStyle(Paint.Style.FILL);
        selfDotPaint.setShadowLayer(12, 0, 0, 0xFF33FF33);

        gwDotPaint.setColor(0xFF33CCFF);
        gwDotPaint.setStyle(Paint.Style.FILL);
        gwDotPaint.setShadowLayer(10, 0, 0, 0xFF33CCFF);

        linePaint.setColor(0xFF1a5a1a);
        linePaint.setStyle(Paint.Style.STROKE);
        linePaint.setStrokeWidth(2f);

        textPaint.setColor(0xFF33FF33);
        textPaint.setTextSize(28f);
        textPaint.setTypeface(Typeface.create(Typeface.MONOSPACE, Typeface.BOLD));
        textPaint.setFakeBoldText(true);
        textPaint.setShadowLayer(4, 0, 0, 0xFF33FF33);

        dimTextPaint.setColor(0xFF33AA33);
        dimTextPaint.setTextSize(24f);
        dimTextPaint.setTypeface(Typeface.MONOSPACE);

        glowPaint.setColor(0x2A33FF33);
        glowPaint.setStyle(Paint.Style.FILL);

        linkTextPaint.setColor(0xFF66FF66);
        linkTextPaint.setTextSize(22f);
        linkTextPaint.setTypeface(Typeface.MONOSPACE);
        linkTextPaint.setTextAlign(Paint.Align.CENTER);
        linkTextPaint.setShadowLayer(3, 0, 0, 0xFF33FF33);

        setLayerType(LAYER_TYPE_SOFTWARE, null);
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
        canvas.drawColor(0xFF0a0f0a);

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
            labels.add(gw ? label + " GW" : label);
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
                if (tp > 0) {
                    int alpha = Math.min(255, (int) (tp * 25) + 80);
                    linePaint.setColor((alpha << 24) | 0x33FF33);
                    linePaint.setStrokeWidth(Math.min(6f, (float) tp / 2f + 2f));
                } else {
                    linePaint.setColor(0x801a5a1a);
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
                Paint offPaint = new Paint(dotPaint);
                offPaint.setColor(0xFF994444);
                canvas.drawCircle(px[i], py[i], dotR - 1, offPaint);
            }

            float tx = px[i] + dotR + 4;
            float ty = py[i] + 5;
            if (px[i] > cx) {
                tx = px[i] - textPaint.measureText(labels.get(i)) - dotR - 4;
            }
            Paint lp = online.get(i) ? textPaint : dimTextPaint;
            canvas.drawText(labels.get(i), tx, ty, lp);
        }

        int onCount = (int) online.stream().filter(b -> b).count();
        String countStr = String.format(Locale.US, "%d/%d NODES", onCount, n);
        if (eudCount > 0) countStr += String.format(Locale.US, "  %d EUD", eudCount);
        canvas.drawText(countStr, 8, h - 8, dimTextPaint);
    }
}
