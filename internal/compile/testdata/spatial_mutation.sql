-- field: createPlace
WITH __mut AS (
  INSERT INTO "public"."places" ("area", "location", "name")
  VALUES ((ST_GeomFromEWKT($1))::geography, ST_SetSRID(ST_GeomFromGeoJSON($2), 4326), $3)
  RETURNING "id", "name", "location", "area"
)
SELECT jsonb_build_object('place', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'name', __t_1."name", 'location', CASE WHEN __t_1."location" IS NULL THEN NULL ELSE ST_AsGeoJSON(__t_1."location")::jsonb END, 'areaArea', ST_Area(__t_1."area")) AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["SRID=4326;POLYGON((-0.130 51.507,-0.126 51.507,-0.126 51.509,-0.130 51.507))","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}","Trafalgar Square"]

-- field: updatePlaceById
WITH __mut AS (
  UPDATE "public"."places"
  SET "location" = ST_GeomFromEWKT($1)
  WHERE "public"."places"."id" = $2
  RETURNING "id", "location"
)
SELECT jsonb_build_object('place', __row.data) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'location', CASE WHEN __t_1."location" IS NULL THEN NULL ELSE ST_AsGeoJSON(__t_1."location")::jsonb END) AS data
  FROM __mut AS __t_1
) AS __row
-- args: ["POINT(1 2)",1]

