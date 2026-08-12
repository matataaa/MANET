package eu.frosttechnologies.meshctrl;

import android.content.Intent;
import android.os.Bundle;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.*;

import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executors;

import javax.net.ssl.*;

public class NodePickerActivity extends AppCompatActivity {

    private ListView listView;
    private EditText customUrl;
    private ProgressBar progress;
    private final List<NodeItem> nodes = new ArrayList<>();
    private String currentUrl;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_node_picker);

        currentUrl = getIntent().getStringExtra("current_url");
        listView = findViewById(R.id.node_list);
        customUrl = findViewById(R.id.custom_url);
        progress = findViewById(R.id.picker_progress);

        if (currentUrl != null) {
            customUrl.setText(currentUrl);
        }

        findViewById(R.id.btn_connect).setOnClickListener(v -> {
            String url = customUrl.getText().toString().trim();
            if (!url.isEmpty()) {
                if (!url.startsWith("http")) url = "https://" + url;
                returnResult(url);
            }
        });

        listView.setOnItemClickListener((parent, view, position, id) -> {
            NodeItem item = nodes.get(position);
            returnResult("https://" + item.ip);
        });

        discoverNodes();
    }

    private void returnResult(String url) {
        Intent data = new Intent();
        data.putExtra("node_url", url);
        setResult(RESULT_OK, data);
        finish();
    }

    private void discoverNodes() {
        if (currentUrl == null || currentUrl.isEmpty()) {
            progress.setVisibility(View.GONE);
            return;
        }

        progress.setVisibility(View.VISIBLE);
        Executors.newSingleThreadExecutor().execute(() -> {
            try {
                String baseUrl = currentUrl.replaceAll("/$", "");
                String json = fetchJson(baseUrl + "/api/data");
                JSONObject data = new JSONObject(json);
                JSONArray nodesArr = data.getJSONArray("nodes");

                List<NodeItem> found = new ArrayList<>();
                for (int i = 0; i < nodesArr.length(); i++) {
                    JSONObject n = nodesArr.getJSONObject(i);
                    String hostname = n.optString("hostname", n.optString("id", "unknown"));
                    String ip = n.optString("ip", "");
                    boolean isMe = n.optBoolean("is_me", false);
                    boolean isGw = n.optBoolean("is_gateway", false);
                    if (!ip.isEmpty()) {
                        found.add(new NodeItem(hostname, ip, isMe, isGw));
                    }
                }

                runOnUiThread(() -> {
                    progress.setVisibility(View.GONE);
                    nodes.clear();
                    nodes.addAll(found);
                    listView.setAdapter(new NodeAdapter());
                });
            } catch (Exception e) {
                runOnUiThread(() -> {
                    progress.setVisibility(View.GONE);
                    Toast.makeText(this, "Could not discover nodes", Toast.LENGTH_SHORT).show();
                });
            }
        });
    }

    private String fetchJson(String urlStr) throws Exception {
        TrustManager[] trustAll = new TrustManager[]{new X509TrustManager() {
            public void checkClientTrusted(java.security.cert.X509Certificate[] c, String a) {}
            public void checkServerTrusted(java.security.cert.X509Certificate[] c, String a) {}
            public java.security.cert.X509Certificate[] getAcceptedIssuers() { return new java.security.cert.X509Certificate[0]; }
        }};
        SSLContext sc = SSLContext.getInstance("TLS");
        sc.init(null, trustAll, new java.security.SecureRandom());

        URL url = new URL(urlStr);
        HttpURLConnection conn;
        if (url.getProtocol().equals("https")) {
            HttpsURLConnection https = (HttpsURLConnection) url.openConnection();
            https.setSSLSocketFactory(sc.getSocketFactory());
            https.setHostnameVerifier((h, s) -> true);
            conn = https;
        } else {
            conn = (HttpURLConnection) url.openConnection();
        }
        conn.setConnectTimeout(5000);
        conn.setReadTimeout(5000);

        BufferedReader reader = new BufferedReader(new InputStreamReader(conn.getInputStream()));
        StringBuilder sb = new StringBuilder();
        String line;
        while ((line = reader.readLine()) != null) sb.append(line);
        reader.close();
        return sb.toString();
    }

    private class NodeAdapter extends BaseAdapter {
        @Override public int getCount() { return nodes.size(); }
        @Override public Object getItem(int i) { return nodes.get(i); }
        @Override public long getItemId(int i) { return i; }

        @Override
        public View getView(int position, View convertView, ViewGroup parent) {
            if (convertView == null) {
                convertView = LayoutInflater.from(NodePickerActivity.this)
                        .inflate(android.R.layout.simple_list_item_2, parent, false);
            }
            NodeItem item = nodes.get(position);
            TextView t1 = convertView.findViewById(android.R.id.text1);
            TextView t2 = convertView.findViewById(android.R.id.text2);

            String label = item.hostname;
            if (item.isMe) label += " (THIS NODE)";
            if (item.isGw) label += " [GW]";
            t1.setText(label);
            t1.setTextColor(0xFFE4E4E7);
            t2.setText(item.ip);
            t2.setTextColor(0xFF6B7280);
            return convertView;
        }
    }

    private static class NodeItem {
        final String hostname, ip;
        final boolean isMe, isGw;
        NodeItem(String hostname, String ip, boolean isMe, boolean isGw) {
            this.hostname = hostname;
            this.ip = ip;
            this.isMe = isMe;
            this.isGw = isGw;
        }
    }
}
