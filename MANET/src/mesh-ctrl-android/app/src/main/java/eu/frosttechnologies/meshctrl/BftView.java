package eu.frosttechnologies.meshctrl;

import android.content.Context;
import android.graphics.Canvas;
import android.graphics.Paint;
import android.graphics.Typeface;
import android.view.View;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

public class BftView extends View {

    private final Paint gridPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint nodePaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint selfPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint labelPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint dimPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint ringPaint = new Paint(Paint.ANTI_ALIAS_FLAG);

    private boolean nvgMode = false;
    private JSONArray nodes;
    private long timestamp;
    private double selfLat = Double.NaN, selfLon = Double.NaN;

    public BftView(Context context) {
        super(context);
        applyColorScheme();
        setLayerType(LAYER_TYPE_SOFTWARE, null);
    }

    public void setNvgMode(boolean nvg) {
        this.nvgMode = nvg;
        applyColorScheme();
        invalidate();
    }

    private void applyColorScheme() {
        int pri = nvgMode ? 0xFFCC3333 : 0xFF33FF33;
        int dim = nvgMode ? 0xFF3a1a1a : 0xFF1a3a1a;
        int mid = nvgMode ? 0xFF8a2222 : 0xFF228a22;

        gridPaint.setColor(dim);
        gridPaint.setStyle(Paint.Style.STROKE);
        gridPaint.setStrokeWidth(1f);

        ringPaint.setColor(mid);
        ringPaint.setStyle(Paint.Style.STROKE);
        ringPaint.setStrokeWidth(1f);

        nodePaint.setColor(pri);
        nodePaint.setStyle(Paint.Style.FILL);

        selfPaint.setColor(pri);
        selfPaint.setStyle(Paint.Style.FILL);
        selfPaint.setShadowLayer(10, 0, 0, pri);

        labelPaint.setColor(pri);
        labelPaint.setTextSize(22f);
        labelPaint.setTypeface(Typeface.MONOSPACE);
        labelPaint.setShadowLayer(3, 0, 0, pri);

        dimPaint.setColor(mid);
        dimPaint.setTextSize(18f);
        dimPaint.setTypeface(Typeface.MONOSPACE);
        dimPaint.setTextAlign(Paint.Align.CENTER);
    }

    public void setData(JSONArray nodes, long timestamp) {
        this.nodes = nodes;
        this.timestamp = timestamp;
        invalidate();
    }

    @Override
    protected void onDraw(Canvas canvas) {
        super.onDraw(canvas);
        canvas.drawColor(nvgMode ? 0xFF0f0a0a : 0xFF0a0f0a);

        int w = getWidth();
        int h = getHeight();
        float cx = w / 2f;
        float cy = h / 2f;

        float maxR = Math.min(w, h) / 2f - 30;
        for (int i = 1; i <= 3; i++) {
            float r = maxR * i / 3f;
            canvas.drawCircle(cx, cy, r, ringPaint);
        }
        canvas.drawLine(cx, 10, cx, h - 10, gridPaint);
        canvas.drawLine(10, cy, w - 10, cy, gridPaint);

        if (nodes == null || nodes.length() == 0) {
            canvas.drawText("NO GPS DATA", cx - 60, cy, labelPaint);
            return;
        }

        List<double[]> positions = new ArrayList<>();
        List<String> names = new ArrayList<>();
        List<Boolean> isSelf = new ArrayList<>();

        selfLat = Double.NaN;
        selfLon = Double.NaN;

        for (int i = 0; i < nodes.length(); i++) {
            JSONObject nd = nodes.optJSONObject(i);
            if (nd == null) continue;
            double lat = nd.optDouble("lat", Double.NaN);
            double lon = nd.optDouble("lon", Double.NaN);
            if (Double.isNaN(lat) || Double.isNaN(lon)) continue;
            if (lat == 0 && lon == 0) continue;

            boolean me = nd.optBoolean("is_me", false);
            if (me) { selfLat = lat; selfLon = lon; }

            String hostname = nd.optString("hostname", "?");
            if (hostname.length() > 8) hostname = hostname.substring(0, 8);

            positions.add(new double[]{lat, lon});
            names.add(hostname);
            isSelf.add(me);
        }

        if (positions.isEmpty()) {
            canvas.drawText("NO GPS DATA", cx - 60, cy, labelPaint);
            return;
        }

        if (Double.isNaN(selfLat)) {
            selfLat = positions.get(0)[0];
            selfLon = positions.get(0)[1];
        }

        double maxDist = 100;
        for (double[] pos : positions) {
            double d = haversine(selfLat, selfLon, pos[0], pos[1]);
            if (d > maxDist) maxDist = d;
        }
        maxDist = Math.max(maxDist * 1.3, 50);

        for (int i = 0; i < positions.size(); i++) {
            double[] pos = positions.get(i);
            double dist = haversine(selfLat, selfLon, pos[0], pos[1]);
            double bearing = bearing(selfLat, selfLon, pos[0], pos[1]);

            float r = (float) (dist / maxDist) * maxR;
            float nx = cx + r * (float) Math.sin(Math.toRadians(bearing));
            float ny = cy - r * (float) Math.cos(Math.toRadians(bearing));

            if (isSelf.get(i)) {
                canvas.drawCircle(nx, ny, 8, selfPaint);
                canvas.drawText(names.get(i), nx + 12, ny + 5, labelPaint);
            } else {
                canvas.drawCircle(nx, ny, 6, nodePaint);
                canvas.drawText(names.get(i), nx + 10, ny + 5, labelPaint);
                String distStr = dist < 1000 ?
                        String.format(Locale.US, "%dm", (int) dist) :
                        String.format(Locale.US, "%.1fkm", dist / 1000);
                canvas.drawText(distStr, nx, ny + 22, dimPaint);
            }
        }

        String scaleStr = maxDist < 1000 ?
                String.format(Locale.US, "%.0fm radius", maxDist) :
                String.format(Locale.US, "%.1fkm radius", maxDist / 1000);
        canvas.drawText(scaleStr, cx, h - 12, dimPaint);

        canvas.drawText("N", cx - 4, 22, labelPaint);
    }

    private static double haversine(double lat1, double lon1, double lat2, double lon2) {
        double R = 6371000;
        double dLat = Math.toRadians(lat2 - lat1);
        double dLon = Math.toRadians(lon2 - lon1);
        double a = Math.sin(dLat / 2) * Math.sin(dLat / 2) +
                Math.cos(Math.toRadians(lat1)) * Math.cos(Math.toRadians(lat2)) *
                        Math.sin(dLon / 2) * Math.sin(dLon / 2);
        return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
    }

    private static double bearing(double lat1, double lon1, double lat2, double lon2) {
        double dLon = Math.toRadians(lon2 - lon1);
        double y = Math.sin(dLon) * Math.cos(Math.toRadians(lat2));
        double x = Math.cos(Math.toRadians(lat1)) * Math.sin(Math.toRadians(lat2)) -
                Math.sin(Math.toRadians(lat1)) * Math.cos(Math.toRadians(lat2)) * Math.cos(dLon);
        return (Math.toDegrees(Math.atan2(y, x)) + 360) % 360;
    }
}
