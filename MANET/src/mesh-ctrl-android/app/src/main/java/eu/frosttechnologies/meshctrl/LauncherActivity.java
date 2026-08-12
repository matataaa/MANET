package eu.frosttechnologies.meshctrl;

import android.content.Intent;
import android.content.SharedPreferences;
import android.os.Bundle;

import androidx.appcompat.app.AppCompatActivity;

public class LauncherActivity extends AppCompatActivity {

    private static final String PREFS = "mesh_ctrl";
    private static final String KEY_LAUNCH = "launch_mode";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        String mode = getSharedPreferences(PREFS, MODE_PRIVATE).getString(KEY_LAUNCH, "ask");

        if ("kdu".equals(mode)) {
            startActivity(new Intent(this, EudActivity.class));
            finish();
            return;
        }
        if ("mgmt".equals(mode)) {
            startActivity(new Intent(this, MainActivity.class));
            finish();
            return;
        }

        setContentView(R.layout.activity_launcher);

        findViewById(R.id.launch_mgmt).setOnClickListener(v -> {
            startActivity(new Intent(this, MainActivity.class));
            finish();
        });

        findViewById(R.id.launch_kdu).setOnClickListener(v -> {
            startActivity(new Intent(this, EudActivity.class));
            finish();
        });
    }
}
