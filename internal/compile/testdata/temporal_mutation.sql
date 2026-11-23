-- field: createBookingNote
WITH __p_1 AS (
  SELECT DISTINCT "room_id" FROM "public"."room_bookings" WHERE "room_id" = $4
),
__mut AS (
  INSERT INTO "public"."booking_notes" ("author_id", "note", "valid_at", "room_id") SELECT $1, $2, $3, __p_1."room_id" FROM __p_1 RETURNING *
)
SELECT jsonb_build_object('bookingNote', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'roomBookingsByRoomIdAndValidAt', __l_4.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $6), '[]'::jsonb)) AS data
    FROM (
      SELECT jsonb_build_object('guest', __t_2."guest") AS ndata, row_number() OVER (ORDER BY __t_2."room_id" ASC, __t_2."valid_at" ASC) AS __rn
      FROM "public"."room_bookings" AS __t_2
      WHERE __t_2."room_id" = __t_1."room_id" AND __t_2."valid_at" && __t_1."valid_at"
      ORDER BY __t_2."room_id" ASC, __t_2."valid_at" ASC
      LIMIT $5
    ) AS __s_3
    GROUP BY ()
  ) AS __l_4 ON true
) AS __row
-- args: [1,"Late arrival","[2026-03-02,2026-03-04)",1,101,100]

-- field: createRoomBooking
WITH __mut AS (
  INSERT INTO "public"."room_bookings" ("guest", "room_id", "valid_at") VALUES ($1, $2, $3) RETURNING *
),
__c_1 AS (
  INSERT INTO "public"."booking_notes" ("author_id", "note", "valid_at", "room_id") SELECT $4, $5, $6, __mut."room_id" FROM __mut RETURNING *
),
__vis_2 AS (
  SELECT * FROM __c_1
)
SELECT jsonb_build_object('roomBooking', __row.data) AS data
FROM (
  SELECT jsonb_build_object('guest', __t_1."guest", 'bookingNotesByRoomIdAndValidAt', __l_4.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $8), '[]'::jsonb)) AS data
    FROM (
      SELECT jsonb_build_object('note', __t_2."note") AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM __vis_2 AS __t_2
      WHERE __t_2."room_id" = __t_1."room_id" AND __t_2."valid_at" && __t_1."valid_at"
      ORDER BY __t_2."id" ASC
      LIMIT $7
    ) AS __s_3
    GROUP BY ()
  ) AS __l_4 ON true
) AS __row
-- args: ["Ada",3,"[2026-05-01,2026-05-10)",1,"Early","[2026-05-02,2026-05-03)",101,100]

-- field: updateRoomBookingByRoomIdAndValidAt
WITH __mut AS (
  UPDATE "public"."room_bookings"
  SET "guest" = $1
  WHERE "public"."room_bookings"."room_id" = $2 AND "public"."room_bookings"."valid_at" = $3
  RETURNING "guest"
)
SELECT jsonb_build_object('roomBooking', __row.data) AS data
FROM (
  SELECT jsonb_build_object('guest', __t_1."guest") AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["Grace",1,"[2026-03-01,2026-03-05)"]

