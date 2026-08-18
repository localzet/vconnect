package app.vconnect.tunnel

import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

object FabricBootstrapClient {

    data class ResolvedEdge(
        val edgeId: String,
        val edgeName: String,
        val callLink: String,
        val controlPlatform: CallPlatform,
    )

    fun resolve(
        controllerBaseUrl: String,
        userId: String,
        credential: String,
        controlPlatform: CallPlatform = CallPlatform.TELEMOST,
    ): ResolvedEdge {
        val base = controllerBaseUrl.trim().trimEnd('/')
        require(base.startsWith("https://") || base.startsWith("http://")) {
            "Fabric Controller URL must start with http:// or https://"
        }
        require(userId.isNotBlank()) { "Fabric user is not configured" }
        require(credential.isNotBlank()) { "Fabric credential is not configured" }

        val user = URLEncoder.encode(userId.trim(), Charsets.UTF_8.name())
        val connection = URL("$base/v1/bootstrap?user=$user").openConnection() as HttpURLConnection
        connection.requestMethod = "GET"
        connection.connectTimeout = 8_000
        connection.readTimeout = 8_000
        connection.setRequestProperty("Accept", "application/json")
        connection.setRequestProperty("X-Fabric-Credential", credential.trim())

        try {
            val status = connection.responseCode
            val body = if (status in 200..299) {
                connection.inputStream.bufferedReader().use { it.readText() }
            } else {
                connection.errorStream?.bufferedReader()?.use { it.readText() }.orEmpty()
            }
            if (status !in 200..299) {
                val detail = runCatching { JSONObject(body).optString("error") }.getOrDefault("")
                error("Fabric Controller HTTP $status${detail.takeIf { it.isNotBlank() }?.let { ": $it" }.orEmpty()}")
            }

            val root = JSONObject(body)
            val edges = root.optJSONArray("edges") ?: error("Fabric Controller returned no edge list")
            for (index in 0 until edges.length()) {
                val edge = edges.optJSONObject(index) ?: continue
                if (!edge.optBoolean("online", false)) continue
                val calls = edge.optJSONObject("serviceCalls") ?: continue
                val link = calls.optString(controlPlatform.id).trim()
                if (link.isEmpty()) continue
                return ResolvedEdge(
                    edgeId = edge.optString("id").ifBlank { "unknown" },
                    edgeName = edge.optString("name").ifBlank { edge.optString("id") },
                    callLink = link,
                    controlPlatform = controlPlatform,
                )
            }
            error("No online RU edge exposes ${controlPlatform.id} control call")
        } finally {
            connection.disconnect()
        }
    }
}
