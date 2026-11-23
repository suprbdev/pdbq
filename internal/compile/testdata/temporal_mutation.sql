-- field: createBookingNote
WITH __p_1 AS (
  SELECT DISTINCT "room_id" FROM "public"."room_bookings" WHERE "public"."room_bookings"."room_id" = $6
),
__mut AS (
  INSERT INTO "public"."booking_notes" ("author_id", "note", "valid_at", "room_id") SELECT $1, $2, daterange($3::date, $4::date, $5::text), __p_1."room_id" FROM __p_1 RETURNING "id", "room_id", "valid_at"
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: roomBookingsByRoomIdAndValidAt.connectByRoomId matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('bookingNote', json_build_object('id', __t_1."id", 'roomBookingsByRoomIdAndValidAt', __l_4.data)) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $8), '[]'::json)) AS data
    FROM (
      SELECT json_build_object('guest', __t_2."guest") AS ndata, row_number() OVER (ORDER BY __t_2."room_id" ASC, __t_2."valid_at" ASC) AS __rn
      FROM "public"."room_bookings" AS __t_2
      WHERE __t_2."room_id" = __t_1."room_id" AND __t_2."valid_at" && __t_1."valid_at"
      ORDER BY __t_2."room_id" ASC, __t_2."valid_at" ASC
      LIMIT $7
    ) AS __s_3
    GROUP BY ()
  ) AS __l_4 ON true
) AS __guarded ON true
WHERE __guard.ok
-- args: [1,"Late arrival","2026-03-02","2026-03-04","[)",1,101,100]

-- field: createRoomBooking
WITH __mut AS (
  INSERT INTO "public"."room_bookings" ("guest", "room_id", "valid_at") VALUES ($1, $2, daterange($3::date, $4::date, $5::text)) RETURNING "room_id", "valid_at", "guest"
),
__c_1 AS (
  INSERT INTO "public"."booking_notes" ("author_id", "note", "valid_at", "room_id") SELECT $6, $7, daterange($8::date, $9::date, $10::text), __mut."room_id" FROM __mut RETURNING "id", "room_id", "valid_at", "note"
),
__vis_3 AS NOT MATERIALIZED (
  SELECT "id", "room_id", "valid_at", "note" FROM "public"."booking_notes"
  UNION ALL SELECT "id", "room_id", "valid_at", "note" FROM __c_1
)
SELECT json_build_object('roomBooking', json_build_object('guest', __t_1."guest", 'bookingNotesByRoomIdAndValidAt', __l_4.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $12), '[]'::json)) AS data
  FROM (
    SELECT json_build_object('note', __t_2."note") AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
    FROM __vis_3 AS __t_2
    WHERE __t_2."room_id" = __t_1."room_id" AND __t_2."valid_at" && __t_1."valid_at"
    ORDER BY __t_2."id" ASC
    LIMIT $11
  ) AS __s_3
  GROUP BY ()
) AS __l_4 ON true
-- args: ["Ada",3,"2026-05-01","2026-05-10","[)",1,"Early","2026-05-02","2026-05-03","[)",101,100]

-- field: updateRoomBookingByRoomIdAndValidAt
WITH __mut AS (
  UPDATE "public"."room_bookings"
  SET "guest" = $1
  WHERE "public"."room_bookings"."room_id" = $2 AND "public"."room_bookings"."valid_at" = daterange($3::date, $4::date, $5::text)
  RETURNING "guest"
)
SELECT json_build_object('roomBooking', json_build_object('guest', __t_1."guest")) AS data
FROM __mut AS __t_1
-- args: ["Grace",1,"2026-03-01","2026-03-05","[)"]

