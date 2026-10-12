USE isuride;

-- 初期データの chair_locations から、椅子ごとの最新座標と総移動距離を作る。
-- 総移動距離は元の owner/chairs の集計と同じく、時刻順に並べた前の座標とのマンハッタン距離の合計(最初の座標は 0)
INSERT INTO chair_distances (chair_id, latitude, longitude, total_distance, total_distance_updated_at)
SELECT chair_id, latitude, longitude, total_distance, created_at
FROM (SELECT chair_id,
             latitude,
             longitude,
             created_at,
             rn,
             SUM(IFNULL(distance, 0)) OVER (PARTITION BY chair_id) AS total_distance
      FROM (SELECT chair_id,
                   latitude,
                   longitude,
                   created_at,
                   ABS(latitude - LAG(latitude) OVER w) + ABS(longitude - LAG(longitude) OVER w) AS distance,
                   ROW_NUMBER() OVER (PARTITION BY chair_id ORDER BY created_at DESC)            AS rn
            FROM chair_locations
            WINDOW w AS (PARTITION BY chair_id ORDER BY created_at)) t1) t2
WHERE rn = 1;
