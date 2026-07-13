-- field: createUser
WITH __mut AS (
  INSERT INTO "public"."users" ("address", "email", "prev_addresses")
  VALUES (jsonb_populate_record(NULL::"public"."address", $1::jsonb), $2, coalesce((SELECT array_agg(jsonb_populate_record(NULL::"public"."address", __e_1.v) ORDER BY __e_1.o) FROM jsonb_array_elements($3::jsonb) WITH ORDINALITY AS __e_1(v, o)), ARRAY[]::"public"."address"[]))
  RETURNING *
)
SELECT jsonb_build_object('user', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_2."id", 'address', CASE WHEN (__t_2."address")::text IS NULL THEN NULL ELSE jsonb_build_object('street', (__t_2."address")."street", 'city', (__t_2."address")."city", 'mood', CASE (__t_2."address")."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) END, 'prevAddresses', CASE WHEN __t_2."prev_addresses" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE WHEN ((__t_2."prev_addresses")[__e_3.o])::text IS NULL THEN NULL ELSE jsonb_build_object('city', ((__t_2."prev_addresses")[__e_3.o])."city") END ORDER BY __e_3.o), '[]'::jsonb) FROM (SELECT generate_subscripts(__t_2."prev_addresses", 1) AS o) AS __e_3) END) AS data
  FROM __mut AS __t_2
) AS __row
-- args: ["{\"city\":\"Springfield\",\"mood\":\"happy\",\"street\":\"1 Main St\"}","c@d.e","[{\"city\":\"Shelbyville\"}]"]

