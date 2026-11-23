-- field: allRoomBookings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('guest', __t_1."guest") AS ndata, row_number() OVER (ORDER BY __t_1."room_id" ASC, __t_1."valid_at" ASC) AS __rn
  FROM "public"."room_bookings" AS __t_1
  WHERE (__t_1."room_id", __t_1."valid_at") > ($1, daterange($2::date, $3::date, $4::text))
  ORDER BY __t_1."room_id" ASC, __t_1."valid_at" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["1","2026-03-01","2026-03-05","[)",2]

-- field: allBookingNotes
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('note', __t_1."note") AS ndata, row_number() OVER (ORDER BY (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."booking_notes" AS __t_1
  WHERE (((SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") < (SELECT (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __a."room_id" AND __ro."valid_at" && __a."valid_at") FROM "public"."booking_notes" AS __a WHERE __a."id" = $1) OR ((SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") IS NULL AND (SELECT (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __a."room_id" AND __ro."valid_at" && __a."valid_at") FROM "public"."booking_notes" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR ((SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") IS NOT DISTINCT FROM (SELECT (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __a."room_id" AND __ro."valid_at" && __a."valid_at") FROM "public"."booking_notes" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2))
  ORDER BY (SELECT MAX(__ro."room_id") FROM "public"."room_bookings" AS __ro WHERE __ro."room_id" = __t_1."room_id" AND __ro."valid_at" && __t_1."valid_at") DESC NULLS LAST, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["1","1",2]

