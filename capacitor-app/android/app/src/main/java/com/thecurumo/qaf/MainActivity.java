package com.thecurumo.qaf;

import android.os.Bundle;
import androidx.appcompat.app.ActionBar;
import androidx.core.view.WindowCompat;
import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    @Override
    public void onCreate(Bundle savedInstanceState) {
        // باید قبل از super.onCreate صدا زده بشه تا decor view درست تنظیم بشه
        WindowCompat.setDecorFitsSystemWindows(getWindow(), false);

        registerPlugin(DatabaseSetupPlugin.class);
        super.onCreate(savedInstanceState);

        // خاموش‌کردن ActionBar، صرف‌نظر از اینکه کدام تم اعمال شده باشد
        ActionBar actionBar = getSupportActionBar();
        if (actionBar != null) {
            actionBar.hide();
        }

        // مخفی‌کردن اسکرول‌بار native خودِ WebView
        bridge.getWebView().setVerticalScrollBarEnabled(false);
        bridge.getWebView().setHorizontalScrollBarEnabled(false);

        // اجازه اتصال مستقیم به سرورهای HTTP بدون خطای Mixed Content
        bridge.getWebView().getSettings().setMixedContentMode(android.webkit.WebSettings.MIXED_CONTENT_ALWAYS_ALLOW);
    }
}