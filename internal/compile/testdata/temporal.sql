-- field: allBookingNotes
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_13.ndata ORDER BY __s_13.__rn ASC) FILTER (WHERE __s_13.__rn <= $6), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('note', __t_1."note", 'validAt', (SELECT jsonb_build_object('start', CASE WHEN lower(__rg_2.r) IS NULL THEN NULL ELSE jsonb_build_object('value', lower(__rg_2.r)) END, 'end', CASE WHEN upper(__rg_2.r) IS NULL THEN NULL ELSE jsonb_build_object('value', upper(__rg_2.r), 'inclusive', upper_inc(__rg_2.r)) END) FROM (SELECT __t_1."valid_at" AS r) AS __rg_2 WHERE __rg_2.r IS NOT NULL), 'author', __l_4.data, 'roomBookingsByRoomIdAndValidAt', __l_9.data) AS ndata, row_number() OVER (ORDER BY (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."booking_notes" AS __t_1
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('email', __t_3."email") AS data
    FROM "public"."users" AS __t_3
    WHERE __t_3."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_4 ON true
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_7.ndata ORDER BY __s_7.__rn ASC) FILTER (WHERE __s_7.__rn <= $2), '[]'::jsonb), 'totalCount', (SELECT count(*) FROM "public"."room_bookings" AS __c_8
    WHERE __c_8."room_id" = __t_1."room_id" AND __c_8."valid_at" && __t_1."valid_at")::int) AS data
    FROM (
      SELECT jsonb_build_object('guest', __t_5."guest", 'validAt', (SELECT jsonb_build_object('start', CASE WHEN lower(__rg_6.r) IS NULL THEN NULL ELSE jsonb_build_object('value', lower(__rg_6.r)) END) FROM (SELECT __t_5."valid_at" AS r) AS __rg_6 WHERE __rg_6.r IS NOT NULL)) AS ndata, row_number() OVER (ORDER BY __t_5."room_id" ASC, __t_5."valid_at" ASC) AS __rn
      FROM "public"."room_bookings" AS __t_5
      WHERE __t_5."room_id" = __t_1."room_id" AND __t_5."valid_at" && __t_1."valid_at"
      ORDER BY __t_5."room_id" ASC, __t_5."valid_at" ASC
      LIMIT $1
    ) AS __s_7
    GROUP BY ()
  ) AS __l_9 ON true
  WHERE EXISTS (SELECT 1 FROM "public"."users" AS __r_10 WHERE __r_10."id" = __t_1."author_id") AND NOT EXISTS (SELECT 1 FROM "public"."room_bookings" AS __r_11 WHERE __r_11."room_id" = __t_1."room_id" AND __r_11."valid_at" && __t_1."valid_at" AND (__r_11."room_id" = $3)) AND EXISTS (SELECT 1 FROM "public"."room_bookings" AS __r_12 WHERE __r_12."room_id" = __t_1."room_id" AND __r_12."valid_at" && __t_1."valid_at" AND (__r_12."room_id" = $4))
  ORDER BY (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") DESC NULLS LAST, __t_1."id" ASC
  LIMIT $5
) AS __s_13
GROUP BY ()
-- args: [6,5,9,1,101,100]

-- field: allRoomBookings
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('nodeId', replace(encode(convert_to(jsonb_build_array('RoomBooking'::text, __t_1."room_id", __t_1."valid_at")::text, 'UTF8'), 'base64'), E'\n', ''), 'guest', __t_1."guest", 'bookingNotesByRoomIdAndValidAt', __l_4.data) AS ndata, row_number() OVER (ORDER BY __t_1."room_id" ASC, __t_1."valid_at" ASC) AS __rn
  FROM "public"."room_bookings" AS __t_1
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $2), '[]'::jsonb)) AS data
    FROM (
      SELECT jsonb_build_object('note', __t_2."note") AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM "public"."booking_notes" AS __t_2
      WHERE __t_2."room_id" = __t_1."room_id" AND __t_2."valid_at" && __t_1."valid_at"
      ORDER BY __t_2."id" ASC
      LIMIT $1
    ) AS __s_3
    GROUP BY ()
  ) AS __l_4 ON true
  ORDER BY __t_1."room_id" ASC, __t_1."valid_at" ASC
  LIMIT $3
) AS __s_5
GROUP BY ()
-- args: [101,100,101,100]

-- field: roomBookingByRoomId
SELECT jsonb_build_object('guest', __t_1."guest", 'validAt', (SELECT jsonb_build_object('end', CASE WHEN upper(__rg_2.r) IS NULL THEN NULL ELSE jsonb_build_object('value', upper(__rg_2.r)) END) FROM (SELECT __t_1."valid_at" AS r) AS __rg_2 WHERE __rg_2.r IS NOT NULL)) AS data
FROM "public"."room_bookings" AS __t_1
WHERE __t_1."room_id" = $1 AND __t_1."valid_at" @> $2::date
LIMIT 1
-- args: [1,"2026-03-02"]

-- field: allRoomBookings
SELECT jsonb_build_object('totalCount', (SELECT count(*) FROM "public"."room_bookings" AS __c_4
WHERE NOT EXISTS (SELECT 1 FROM "public"."booking_notes" AS __r_5 WHERE __r_5."room_id" = __c_4."room_id" AND __r_5."valid_at" && __c_4."valid_at"))::int) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."room_id" ASC, __t_1."valid_at" ASC) AS __rn
  FROM "public"."room_bookings" AS __t_1
  WHERE NOT EXISTS (SELECT 1 FROM "public"."booking_notes" AS __r_2 WHERE __r_2."room_id" = __t_1."room_id" AND __r_2."valid_at" && __t_1."valid_at")
  ORDER BY __t_1."room_id" ASC, __t_1."valid_at" ASC
  LIMIT $1
) AS __s_3
GROUP BY ()
-- args: [101]

-- field: allBookingNotes
SELECT jsonb_build_object('totalCount', (SELECT count(*) FROM "public"."booking_notes" AS __c_4
WHERE NOT EXISTS (SELECT 1 FROM "public"."room_bookings" AS __r_5 WHERE __r_5."room_id" = __c_4."room_id" AND __r_5."valid_at" && __c_4."valid_at"))::int) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."booking_notes" AS __t_1
  WHERE NOT EXISTS (SELECT 1 FROM "public"."room_bookings" AS __r_2 WHERE __r_2."room_id" = __t_1."room_id" AND __r_2."valid_at" && __t_1."valid_at")
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_3
GROUP BY ()
-- args: [101]

-- field: allRoomBookings
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('guest', __t_1."guest") AS ndata, row_number() OVER (ORDER BY (SELECT MIN(__ro."id") FROM "public"."booking_notes" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") ASC, __t_1."room_id" ASC, __t_1."valid_at" ASC) AS __rn
  FROM "public"."room_bookings" AS __t_1
  ORDER BY (SELECT MIN(__ro."id") FROM "public"."booking_notes" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") ASC, __t_1."room_id" ASC, __t_1."valid_at" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

